package service

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/coneno/logger"
	"github.com/golang/protobuf/ptypes/empty"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/pwhash"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"github.com/influenzanet/user-management-service/pkg/utils"

	constants "github.com/influenzanet/go-utils/pkg/constants"
)

func (s *userManagementServer) Status(ctx context.Context, _ *empty.Empty) (*api.ServiceStatus, error) {
	return &api.ServiceStatus{
		Status:  api.ServiceStatus_NORMAL,
		Msg:     "service running",
		Version: apiVersion,
	}, nil
}

func (s *userManagementServer) SendVerificationCode(ctx context.Context, req *api.SendVerificationCodeReq) (*api.ServiceStatus, error) {
	if req == nil || req.Email == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid username and/or password")
	}

	if req.InstanceId == "" {
		req.InstanceId = "default"
	}

	if !s.isInstanceIDAllowed(req.InstanceId) {
		logger.Warning.Printf("SendVerificationCode: instance ID not allowed: %s", req.InstanceId)
		return nil, status.Error(codes.InvalidArgument, "invalid instance ID")
	}

	req.Email = utils.SanitizeEmail(req.Email)
	user, err := s.userDBservice.GetUserByAccountID(req.InstanceId, req.Email)
	if err != nil {
		logger.Warning.Printf("SECURITY WARNING: login step 1 attempt with wrong email address for %s", req.Email)
		return nil, status.Error(codes.InvalidArgument, "invalid username and/or password")
	}

	if utils.HasMoreAttemptsRecently(user.Account.FailedLoginAttempts, allowedPasswordAttempts, loginFailedAttemptWindow) {
		s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_LOGIN_ATTEMPT_ON_BLOCKED_ACCOUNT, "send verification code endpoint")
		logger.Warning.Printf("SECURITY WARNING: login attempt blocked for email address for %s - too many wrong tries recently", user.ID.Hex())
		time.Sleep(time.Duration(rand.Intn(10)) * time.Second)
		return nil, status.Error(codes.InvalidArgument, "invalid username and/or password")
	}

	if user.Account.VerificationCode.CreatedAt > time.Now().Unix()-loginVerificationCodeCooldown {
		s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_LOGIN_ATTEMPT_ON_BLOCKED_ACCOUNT, "try resending verification code too often")
		logger.Warning.Printf("SECURITY WARNING: resend verification code %s - too many wrong tries recently", req.Email)
		return nil, status.Error(codes.InvalidArgument, "cannot generate verification code so often")
	}

	match, err := pwhash.ComparePasswordWithHash(user.Account.Password, req.Password)
	if err != nil || !match {
		logger.Warning.Printf("SECURITY WARNING: login step 1 attempt with wrong password for %s", user.ID.Hex())
		if err2 := s.userDBservice.SaveFailedLoginAttempt(req.InstanceId, user.ID.Hex()); err != nil {
			logger.Error.Printf("DB ERROR: unexpected error when updating user: %s ", err2.Error())
		}
		s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_AUTH_WRONG_PASSWORD, "send verification code endpoint")
		return nil, status.Error(codes.InvalidArgument, "invalid username and/or password")
	}

	err = s.generateAndSendVerificationCode(req.InstanceId, user)
	if err != nil {
		return nil, err
	}

	return &api.ServiceStatus{
		Version: apiVersion,
		Status:  api.ServiceStatus_NORMAL,
		Msg:     "code generated and message sending triggered",
	}, nil
}

func (s *userManagementServer) AutoValidateTempToken(ctx context.Context, req *api.AutoValidateReq) (*api.AutoValidateResponse, error) {
	if req == nil || req.TempToken == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid token")
	}

	tokenInfos, err := s.ValidateTempToken(req.TempToken,
		[]string{
			constants.TOKEN_PURPOSE_INVITATION,
			constants.TOKEN_PURPOSE_SURVEY_LOGIN,
			constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
		})

	if err != nil {
		if err.Error() == "wrong token" {
			logger.Warning.Printf("SECURITY WARNING: temptoken cannot be found %s", req.TempToken)
		} else if err.Error() == "wrong token purpose" {
			logger.Warning.Printf("SECURITY WARNING: temptoken with wrong prupose found: %s - by user %s in instance %s", tokenInfos.Purpose, tokenInfos.UserID, tokenInfos.InstanceID)
		} else if err.Error() == "token expired" {
			logger.Warning.Printf("SECURITY WARNING: temptoken is expired - by user %s in instance %s", tokenInfos.UserID, tokenInfos.InstanceID)
		} else {
			logger.Warning.Printf("SECURITY WARNING: unexpected error for autovalidating temp token - by user %s in instance %s", tokenInfos.UserID, tokenInfos.InstanceID)
		}
		return nil, status.Error(codes.InvalidArgument, "invalid token")
	}

	user, err := s.userDBservice.GetUserByID(tokenInfos.InstanceID, tokenInfos.UserID)
	if err != nil {
		logger.Error.Printf("unexpected error when retrieving user: %v", err)
		return nil, status.Error(codes.InvalidArgument, "user not found")
	}

	sameUser := false
	if len(req.AccessToken) > 0 {
		validatedToken, _, err := tokens.ValidateToken(req.AccessToken)
		if err != nil && !strings.Contains(err.Error(), "token is expired by") {
			logger.Error.Printf("AutoValidateTempToken: unexpected error when parsing token -> %v", err)
		}
		if validatedToken.ID == tokenInfos.UserID && validatedToken.InstanceID == tokenInfos.InstanceID {
			sameUser = true
		}
	}

	if user.Account.VerificationCode.ExpiresAt > time.Now().Unix()+loginVerificationCodeCooldown {
		logger.Debug.Printf("AutoValidateTempToken: verification code re-used for %s", user.ID.Hex())
		return &api.AutoValidateResponse{AccountId: user.Account.AccountID, IsSameUser: sameUser, VerificationCode: user.Account.VerificationCode.Code, InstanceId: tokenInfos.InstanceID}, nil
	}

	vc, err := tokens.GenerateVerificationCode(6)
	if err != nil {
		logger.Error.Printf("unexpected error while generating verification code for %s: %v", user.ID.Hex(), err)
		return nil, status.Error(codes.Internal, "error while generating verification code")
	}

	user.Account.VerificationCode = models.VerificationCode{
		Code:      vc,
		ExpiresAt: time.Now().Unix() + s.Intervals.VerificationCodeLifetime,
	}
	user, err = s.userDBservice.UpdateUser(tokenInfos.InstanceID, user)
	if err != nil {
		logger.Error.Printf("AutoValidateTempToken: unexpected error when saving user [%s] -> %v", user.ID.Hex(), err)
		return nil, status.Error(codes.Internal, "user couldn't be updated")
	}

	if err := s.globalDBService.DeleteAllTempTokenForUser(tokenInfos.InstanceID, user.ID.Hex(), constants.TOKEN_PURPOSE_INVITATION); err != nil {
		logger.Error.Printf("unexpected error: %s", err.Error())
	}
	if err := s.globalDBService.DeleteAllTempTokenForUser(tokenInfos.InstanceID, user.ID.Hex(), constants.TOKEN_PURPOSE_PASSWORD_RESET); err != nil {
		logger.Error.Printf("unexpected error: %s", err.Error())
	}

	return &api.AutoValidateResponse{AccountId: user.Account.AccountID, IsSameUser: sameUser, VerificationCode: vc, InstanceId: tokenInfos.InstanceID}, nil
}

func (s *userManagementServer) LoginWithEmail(ctx context.Context, req *api.LoginWithEmailMsg) (*api.LoginResponse, error) {
	if req == nil || req.Email == "" || req.Password == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid username and/or password")
	}

	if req.InstanceId == "" {
		req.InstanceId = "default"
	}

	if !s.isInstanceIDAllowed(req.InstanceId) {
		logger.Warning.Printf("LoginWithEmail: instance ID not allowed: %s", req.InstanceId)
		return nil, status.Error(codes.InvalidArgument, "invalid instance ID")
	}

	req.Email = utils.SanitizeEmail(req.Email)
	user, err := s.userDBservice.GetUserByAccountID(req.InstanceId, req.Email)
	if err != nil {
		logger.Warning.Printf("SECURITY WARNING: login attempt with wrong email address for %s", req.Email)
		s.SaveLogEvent(req.InstanceId, "", loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_AUTH_WRONG_ACCOUNT_ID, req.Email)
		return nil, status.Error(codes.InvalidArgument, "invalid username and/or password")
	}

	if utils.HasMoreAttemptsRecently(user.Account.FailedLoginAttempts, allowedPasswordAttempts, loginFailedAttemptWindow) {
		logger.Warning.Printf("SECURITY WARNING: login attempt blocked for email address for %s - too many wrong tries recently", req.Email)

		s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_LOGIN_ATTEMPT_ON_BLOCKED_ACCOUNT, "")
		if err2 := s.userDBservice.SaveFailedLoginAttempt(req.InstanceId, user.ID.Hex()); err != nil {
			logger.Error.Printf("DB ERROR: unexpected error when updating user: %s ", err2.Error())
		}
		time.Sleep(time.Duration(rand.Intn(10)) * time.Second)
		return nil, status.Error(codes.InvalidArgument, "invalid username and/or password")
	}

	if user.Account.Type == models.ACCOUNT_TYPE_EXTERNAL {
		logger.Warning.Printf("[SECURITY WARNING]: invalid login attempt for external account (%s)", req.Email)
		s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_AUTH_WRONG_ACCOUNT_ID, "reason: account id used for external user")
		return nil, status.Error(codes.InvalidArgument, "invalid username and/or password")
	}

	match, err := pwhash.ComparePasswordWithHash(user.Account.Password, req.Password)
	if err != nil || !match {
		logger.Warning.Printf("SECURITY WARNING: login attempt with wrong password for %s", user.ID.Hex())
		s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_AUTH_WRONG_PASSWORD, "")
		if err2 := s.userDBservice.SaveFailedLoginAttempt(req.InstanceId, user.ID.Hex()); err != nil {
			logger.Error.Printf("DB ERROR: unexpected error when updating user: %s ", err2.Error())
		}
		return nil, status.Error(codes.InvalidArgument, "invalid username and/or password")
	}

	if user.Account.AuthType == "2FA" {
		if req.VerificationCode == "" {
			// user tries first step
			if user.Account.VerificationCode.Code == "" || user.Account.VerificationCode.CreatedAt == 0 || user.Account.VerificationCode.ExpiresAt < time.Now().Unix() {
				if user.Account.VerificationCode.CreatedAt > time.Now().Unix()-loginVerificationCodeCooldown {
					s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_LOGIN_ATTEMPT_ON_BLOCKED_ACCOUNT, "try resending verification code too often")
					logger.Warning.Printf("SECURITY WARNING: resend verification code %s - too many wrong tries recently", user.ID.Hex())
					return nil, status.Error(codes.InvalidArgument, "cannot generate verification code so often")
				}
				err = s.generateAndSendVerificationCode(req.InstanceId, user)
				if err != nil {
					logger.Error.Printf("login: unexpected error %v", err)
					return nil, status.Error(codes.InvalidArgument, "code generation error")
				}
			}
			return &api.LoginResponse{
				User: &api.User{
					Account: &api.User_Account{
						AccountConfirmedAt: user.Account.AccountConfirmedAt,
						AccountId:          user.Account.AccountID,
					},
				},
				SecondFactorNeeded: true,
			}, nil
		} else {
			// user tries second step
			if user.Account.VerificationCode.ExpiresAt < time.Now().Unix() || user.Account.VerificationCode.Code != req.VerificationCode {
				logger.Warning.Printf("SECURITY WARNING: login attempt with wrong or expired verification code for %s", user.ID.Hex())
				s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_AUTH_WRONG_VERIFICATION_CODE, "")
				if err2 := s.userDBservice.SaveFailedLoginAttempt(req.InstanceId, user.ID.Hex()); err != nil {
					logger.Error.Printf("DB ERROR: unexpected error when updating user: %s ", err2.Error())
				}

				if user.Account.VerificationCode.Attempts <= allowedVerificationCodeAttempts {
					user.Account.VerificationCode.Attempts += 1
					user, err = s.userDBservice.UpdateUser(req.InstanceId, user)
					if err != nil {
						logger.Error.Printf("LoginWithEmail: unexpected error when saving user -> %v", err)
					}
					return nil, status.Error(codes.InvalidArgument, "wrong verfication code")
				} else {
					if user.Account.VerificationCode.CreatedAt > time.Now().Unix()-loginVerificationCodeCooldown {
						s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_LOGIN_ATTEMPT_ON_BLOCKED_ACCOUNT, "try resending verification code too often")
						logger.Warning.Printf("SECURITY WARNING: resend verification code %s - too many wrong tries recently", user.ID.Hex())
						return nil, status.Error(codes.InvalidArgument, "cannot generate verification code so often")
					}
					err = s.generateAndSendVerificationCode(req.InstanceId, user)
					if err != nil {
						logger.Error.Printf("login: unexpected error %v", err)
						return nil, status.Error(codes.InvalidArgument, "code generation error")
					}
					return nil, status.Error(codes.InvalidArgument, "new verification code")
				}
			}
		}
	}

	var username string
	currentRoles := user.Roles
	if req.AsParticipant {
		currentRoles = []string{constants.USER_ROLE_PARTICIPANT}
	} else {
		if len(user.Roles) > 1 || len(user.Roles) == 1 && user.Roles[0] != constants.USER_ROLE_PARTICIPANT {
			username = user.Account.AccountID
		}
	}

	apiUser := user.ToAPI()

	mainProfileID, otherProfileIDs := utils.GetMainAndOtherProfiles(user)

	// Access Token
	token, err := tokens.GenerateNewToken(
		apiUser.Id,
		apiUser.Account.AccountConfirmedAt > 0,
		mainProfileID,
		currentRoles,
		req.InstanceId,
		s.Intervals.TokenExpiryInterval,
		username,
		nil,
		otherProfileIDs,
	)
	if err != nil {
		logger.Error.Printf("LoginWithEmail: unexpected error during token generation -> %v", err)
		return nil, status.Error(codes.Internal, "token generation error")
	}

	// Refresh Token
	rt, err := tokens.GenerateUniqueTokenString()
	if err != nil {
		logger.Error.Printf("LoginWithEmail: unexpected error during refresh token generation -> %v", err)
		return nil, status.Error(codes.Internal, "token generation error")
	}
	user.AddRefreshToken(rt)
	user.Timestamps.LastLogin = time.Now().Unix()
	user.Account.VerificationCode = models.VerificationCode{}
	user.Account.FailedLoginAttempts = utils.RemoveAttemptsOlderThan(user.Account.FailedLoginAttempts, 3600)
	user.Account.PasswordResetTriggers = utils.RemoveAttemptsOlderThan(user.Account.PasswordResetTriggers, 7200)

	user, err = s.userDBservice.UpdateUser(req.InstanceId, user)
	if err != nil {
		logger.Error.Printf("LoginWithEmail: unexpected error when saving user -> %v", err)
		return nil, status.Error(codes.Internal, "user couldn't be updated")
	}

	// remove all temptokens for password reset:
	if err := s.globalDBService.DeleteAllTempTokenForUser(req.InstanceId, user.ID.Hex(), constants.TOKEN_PURPOSE_PASSWORD_RESET); err != nil {
		logger.Error.Printf("LoginWithEmail: %s", err.Error())
	}

	s.SaveLogEvent(req.InstanceId, apiUser.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_LOGIN_SUCCESS, "")

	response := &api.LoginResponse{
		Token: &api.TokenResponse{
			AccessToken:       token,
			RefreshToken:      rt,
			ExpiresIn:         int32(s.Intervals.TokenExpiryInterval / time.Minute),
			Profiles:          apiUser.Profiles,
			SelectedProfileId: mainProfileID,
			PreferredLanguage: apiUser.Account.PreferredLanguage,
		},
		User: user.ToAPI(),
	}
	return response, nil

}

func (s *userManagementServer) LoginWithExternalIDP(ctx context.Context, req *api.LoginWithExternalIDPMsg) (*api.LoginResponse, error) {
	if req == nil || req.Email == "" || req.InstanceId == "" {
		logger.Error.Printf("[ERROR] LoginWithExternalIDP: invalid request - %v", req)
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if !s.isInstanceIDAllowed(req.InstanceId) {
		logger.Warning.Printf("LoginWithExternalIDP: instance ID not allowed: %s", req.InstanceId)
		return nil, status.Error(codes.InvalidArgument, "invalid instance ID")
	}

	req.Email = utils.SanitizeEmail(req.Email)
	user, err := s.userDBservice.GetUserByAccountID(req.InstanceId, req.Email)
	if err != nil {
		// user does not exists - create user
		randomPW, err := tokens.GenerateUniqueTokenString()
		if err != nil {
			logger.Error.Printf("[ERROR] LoginWithExternalIDP: random pw error - %v", err)
		}
		// Create user DB object from request:
		user = models.User{
			Account: models.Account{
				Type:                  models.ACCOUNT_TYPE_EXTERNAL,
				AccountID:             req.Email,
				AccountConfirmedAt:    time.Now().Unix(),
				Password:              randomPW, // not used, just to not leave it empty
				PreferredLanguage:     "",
				FailedLoginAttempts:   []int64{},
				PasswordResetTriggers: []int64{},
			},
			Roles: []string{req.Role},
			Profiles: []models.Profile{
				{
					ID:                 primitive.NewObjectID(),
					Alias:              utils.BlurEmailAddress(req.Email),
					ConsentConfirmedAt: time.Now().Unix(),
					AvatarID:           "default",
					MainProfile:        true,
				},
			},
			Timestamps: models.Timestamps{
				CreatedAt: time.Now().Unix(),
			},
		}
		user.AddNewEmail(req.Email, false)

		user.Account.AuthType = req.Customer
		user.ContactPreferences.SubscribedToNewsletter = false
		user.ContactPreferences.SendNewsletterTo = []string{user.ContactInfos[0].ID.Hex()}

		// on which weekday the user will receive the reminder emails
		user.ContactPreferences.SubscribedToWeekly = false
		user.ContactPreferences.ReceiveWeeklyMessageDayOfWeek = int32(s.weekdayStrategy.Weekday())

		id, err := s.userDBservice.AddUser(req.InstanceId, user)
		if err != nil {
			logger.Error.Printf("ERROR: when creating new user: %s", err.Error())
			return nil, status.Error(codes.Internal, "user creation failed")
		}
		user.ID, _ = primitive.ObjectIDFromHex(id)

	} else {
		if user.Account.Type != models.ACCOUNT_TYPE_EXTERNAL {
			logger.Error.Printf("[ERROR] LoginWithExternalIDP: wrong account type '%s' for %v", user.Account.Type, req)
			s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_ERROR, constants.LOG_EVENT_AUTH_WRONG_ACCOUNT_ID, "wrong account type for external login: "+user.Account.Type)
			return nil, status.Error(codes.PermissionDenied, "wrong account type")
		}

		if !user.HasRole(req.Role) {
			user.Roles = append(user.Roles, req.Role)
		}
	}

	username := user.Account.AccountID
	currentRoles := []string{req.Role}

	apiUser := user.ToAPI()

	mainProfileID, otherProfileIDs := utils.GetMainAndOtherProfiles(user)

	// Access Token
	token, err := tokens.GenerateNewToken(
		apiUser.Id,
		apiUser.Account.AccountConfirmedAt > 0,
		mainProfileID,
		currentRoles,
		req.InstanceId,
		s.Intervals.TokenExpiryInterval,
		username,
		nil,
		otherProfileIDs,
	)
	if err != nil {
		logger.Error.Printf("[ERROR] LoginWithExternalIDP: unexpected error during token generation -> %v", err)
		return nil, status.Error(codes.Internal, "token generation error")
	}

	// Refresh Token
	rt, err := tokens.GenerateUniqueTokenString()
	if err != nil {
		logger.Error.Printf("[ERROR] LoginWithExternalIDP: unexpected error during refresh token generation -> %v", err)
		return nil, status.Error(codes.Internal, "token generation error")
	}
	user.AddRefreshToken(rt)
	user.Timestamps.LastLogin = time.Now().Unix()
	user.Account.VerificationCode = models.VerificationCode{}
	user.Account.FailedLoginAttempts = utils.RemoveAttemptsOlderThan(user.Account.FailedLoginAttempts, 3600)
	user.Account.PasswordResetTriggers = utils.RemoveAttemptsOlderThan(user.Account.PasswordResetTriggers, 7200)

	user, err = s.userDBservice.UpdateUser(req.InstanceId, user)
	if err != nil {
		logger.Error.Printf("[ERROR] LoginWithExternalIDP: unexpected error when saving user -> %v", err)
		return nil, status.Error(codes.Internal, "user couldn't be updated")
	}

	// remove all temptokens for password reset:
	if err := s.globalDBService.DeleteAllTempTokenForUser(req.InstanceId, user.ID.Hex(), constants.TOKEN_PURPOSE_PASSWORD_RESET); err != nil {
		logger.Error.Printf("[ERROR] LoginWithExternalIDP: %s", err.Error())
	}

	msg := fmt.Sprintf("User: %s\nIDP: %s\nGroup info: %s", req.Idp, req.GroupInfo, user.Account.AccountID)
	s.SaveLogEvent(req.InstanceId, apiUser.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_LOGIN_SUCCESS, msg)

	response := &api.LoginResponse{
		Token: &api.TokenResponse{
			AccessToken:       token,
			RefreshToken:      rt,
			ExpiresIn:         int32(s.Intervals.TokenExpiryInterval / time.Minute),
			Profiles:          apiUser.Profiles,
			SelectedProfileId: mainProfileID,
			PreferredLanguage: apiUser.Account.PreferredLanguage,
		},
		User: user.ToAPI(),
	}
	return response, nil
}

func (s *userManagementServer) SignupWithEmail(ctx context.Context, req *api.SignupWithEmailMsg) (*api.TokenResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	req.Email = utils.SanitizeEmail(req.Email)
	if !utils.CheckEmailFormat(req.Email) {
		return nil, status.Error(codes.InvalidArgument, "email not valid")
	}
	if !utils.CheckLanguageCode(req.PreferredLanguage) {
		return nil, status.Error(codes.InvalidArgument, "language code wrong")
	}
	if !utils.CheckPasswordFormat(req.Password) {
		return nil, status.Error(codes.InvalidArgument, "password too weak")
	}

	if req.InstanceId == "" {
		req.InstanceId = "default"
	}

	if !s.isInstanceIDAllowed(req.InstanceId) {
		logger.Warning.Printf("SignupWithEmail: instance ID not allowed: %s", req.InstanceId)
		return nil, status.Error(codes.InvalidArgument, "invalid instance ID")
	}

	newUserCount, err := s.userDBservice.CountRecentlyCreatedUsers(req.InstanceId, signupRateLimitWindow)
	if err != nil {
		logger.Error.Printf("ERROR: signup - unexpected error when counting: %v", err)
	} else {
		if newUserCount > s.newUserCountLimit {
			logger.Warning.Println("ERROR: user creation blocked due to too many registations")
			return nil, status.Error(codes.Internal, "user creation failed, please try in some minutes again")
		}
	}

	password, err := pwhash.HashPassword(req.Password)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Create user DB object from request:
	newUser := models.User{
		Account: models.Account{
			Type:                  models.ACCOUNT_TYPE_EMAIL,
			AccountID:             req.Email,
			AccountConfirmedAt:    0, // not confirmed yet
			Password:              password,
			PreferredLanguage:     req.PreferredLanguage,
			FailedLoginAttempts:   []int64{},
			PasswordResetTriggers: []int64{},
		},
		Roles: []string{constants.USER_ROLE_PARTICIPANT},
		Profiles: []models.Profile{
			{
				ID:                 primitive.NewObjectID(),
				Alias:              utils.BlurEmailAddress(req.Email),
				ConsentConfirmedAt: time.Now().Unix(),
				AvatarID:           "default",
				MainProfile:        true,
			},
		},
		Timestamps: models.Timestamps{
			CreatedAt: time.Now().Unix(),
		},
	}
	newUser.AddNewEmail(req.Email, false)
	if req.Use_2Fa {
		newUser.Account.AuthType = "2FA"
	}

	if req.WantsNewsletter {
		newUser.ContactPreferences.SubscribedToNewsletter = true
		newUser.ContactPreferences.SendNewsletterTo = []string{newUser.ContactInfos[0].ID.Hex()}
	}
	// on which weekday the user will receive the reminder emails
	newUser.ContactPreferences.SubscribedToWeekly = true
	newUser.ContactPreferences.ReceiveWeeklyMessageDayOfWeek = int32(s.weekdayStrategy.Weekday())

	id, err := s.userDBservice.AddUser(req.InstanceId, newUser)
	if err != nil {
		logger.Error.Printf("ERROR: when creating new user: %s", err.Error())
		return nil, status.Error(codes.Internal, "user creation failed")
	}
	newUser.ID, _ = primitive.ObjectIDFromHex(id)

	// TempToken for contact verification:
	tempTokenInfos := models.TempToken{
		UserID:     id,
		InstanceID: req.InstanceId,
		Purpose:    constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
		Info: map[string]string{
			"type":  models.ACCOUNT_TYPE_EMAIL,
			"email": newUser.Account.AccountID,
		},
		Expiration: tokens.GetExpirationTime(s.Intervals.ContactVerificationTokenLifetime),
	}
	tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
	if err != nil {
		logger.Error.Printf("ERROR: signup method failed to create verification token: %s", err.Error())
		return nil, status.Error(codes.Internal, "failed to create verification token")
	}

	// ---> Trigger message sending
	go func(instanceID string, accountID string, tempToken string, preferredLang string) {
		_, err = s.clients.MessagingService.SendInstantEmail(context.TODO(), &messageAPI.SendEmailReq{
			InstanceId:  instanceID,
			To:          []string{accountID},
			MessageType: constants.EMAIL_TYPE_REGISTRATION,
			ContentInfos: map[string]string{
				"token": tempToken,
			},
			PreferredLanguage: preferredLang,
		})
		if err != nil {
			logger.Error.Printf("SignupWithEmail: %s", err.Error())
		}
	}(req.InstanceId, newUser.Account.AccountID, tempToken, newUser.Account.PreferredLanguage)
	// <---

	var username string
	if len(newUser.Roles) > 1 || len(newUser.Roles) == 1 && newUser.Roles[0] != "PARTICIPANT" {
		username = newUser.Account.AccountID
	}
	apiUser := newUser.ToAPI()

	// Access Token
	token, err := tokens.GenerateNewToken(
		apiUser.Id,
		apiUser.Account.AccountConfirmedAt > 0,
		apiUser.Profiles[0].Id,
		newUser.Roles,
		req.InstanceId,
		s.Intervals.TokenExpiryInterval,
		username,
		nil,
		[]string{},
	)
	if err != nil {
		logger.Error.Printf("ERROR: signup method failed to generate jwt: %s", err.Error())
		return nil, status.Error(codes.Internal, "token creation failed")
	}

	// Refresh Token
	rt, err := tokens.GenerateUniqueTokenString()
	if err != nil {
		logger.Error.Printf("ERROR: signup method failed to generate refresh token: %s", err.Error())
		return nil, status.Error(codes.Internal, "token creation failed")
	}
	newUser.AddRefreshToken(rt)
	newUser.Timestamps.LastLogin = time.Now().Unix()

	newUser, err = s.userDBservice.UpdateUser(req.InstanceId, newUser)
	if err != nil {
		logger.Error.Printf("ERROR: signup method failed to save refresh token: %s", err.Error())
		return nil, status.Error(codes.Internal, "user created, but token could not be saved")
	}

	s.SaveLogEvent(req.InstanceId, newUser.ID.Hex(), loggingAPI.LogEventType_LOG, constants.LOG_EVENT_ACCOUNT_CREATED, newUser.Account.AccountID)

	response := &api.TokenResponse{
		AccessToken:       token,
		RefreshToken:      rt,
		ExpiresIn:         int32(s.Intervals.TokenExpiryInterval / time.Minute),
		Profiles:          apiUser.Profiles,
		SelectedProfileId: apiUser.Profiles[0].Id,
		PreferredLanguage: apiUser.Account.PreferredLanguage,
	}
	return response, nil
}

func (s *userManagementServer) VerifyContact(ctx context.Context, req *api.TempToken) (*api.User, error) {
	if req == nil || req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	tokenInfos, err := s.ValidateTempToken(req.Token, []string{
		constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
		constants.TOKEN_PURPOSE_INVITATION,
	})
	if err != nil {
		logger.Error.Printf("VerifyContact: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	user, err := s.userDBservice.GetUserByID(tokenInfos.InstanceID, tokenInfos.UserID)
	if err != nil {
		logger.Error.Printf("VerifyContact: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, "no user found")
	}

	cType, ok1 := tokenInfos.Info["type"]
	email, ok2 := tokenInfos.Info["email"]
	if !ok1 || !ok2 {
		return nil, status.Error(codes.InvalidArgument, "missing token info")
	}

	if err := user.ConfirmContactInfo(cType, email); err != nil {
		logger.Error.Printf("VerifyContact: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if user.Account.Type == models.ACCOUNT_TYPE_EMAIL && user.Account.AccountID == email {
		user.Account.AccountConfirmedAt = time.Now().Unix()
	}
	user, err = s.userDBservice.UpdateUser(tokenInfos.InstanceID, user)

	s.SaveLogEvent(tokenInfos.InstanceID, tokenInfos.UserID, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_CONTACT_VERIFIED, email)
	return user.ToAPI(), err
}

func (s *userManagementServer) ResendContactVerification(ctx context.Context, req *api.ResendContactVerificationReq) (*api.ServiceStatus, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.Address == "" || req.Type == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		logger.Error.Printf("ResendContactVerification: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	ci, found := user.FindContactInfoByTypeAndAddr("email", req.Address)
	if !found {
		return nil, status.Error(codes.InvalidArgument, "address not found")
	}

	if ci.ConfirmationLinkSentAt > time.Now().Unix()-contactVerificationMessageCooldown {
		return nil, status.Error(codes.InvalidArgument, "cannot send verification so often")
	}

	// TempToken for contact verification:
	tempTokenInfos := models.TempToken{
		UserID:     req.Token.Id,
		InstanceID: req.Token.InstanceId,
		Purpose:    constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
		Info: map[string]string{
			"type":  models.ACCOUNT_TYPE_EMAIL,
			"email": ci.Email,
		},
		Expiration: tokens.GetExpirationTime(s.Intervals.ContactVerificationTokenLifetime),
	}
	tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// ---> Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:  req.Token.InstanceId,
		To:          []string{req.Address},
		MessageType: constants.EMAIL_TYPE_VERIFY_EMAIL,
		ContentInfos: map[string]string{
			"token": tempToken,
		},
		PreferredLanguage: user.Account.PreferredLanguage,
	})
	if err != nil {
		logger.Error.Printf("ResendContactVerification: %s", err.Error())
	}
	// <---

	// update last verification email sent time:
	user.SetContactInfoVerificationSent("email", req.Address)
	_, err = s.userDBservice.UpdateUser(req.Token.InstanceId, user)
	if err != nil {
		logger.Error.Printf("ResendContactVerification: %s", err.Error())
	}

	return &api.ServiceStatus{
		Status:  api.ServiceStatus_NORMAL,
		Msg:     "message sent",
		Version: apiVersion,
	}, nil
}
