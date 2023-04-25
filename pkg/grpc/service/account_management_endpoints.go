package service

import (
	"context"
	"strconv"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/go-utils/pkg/constants"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/pwhash"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *userManagementServer) GetUser(ctx context.Context, req *api.UserReference) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if req.UserId == "" {
		req.UserId = req.Token.Id
	}

	if req.Token.Id != req.UserId { // Later can be overwritten
		logger.Warning.Printf("SECURITY WARNING: not authorized GetUser(): %s tried to access %s", req.Token.Id, req.UserId)
		return nil, status.Error(codes.PermissionDenied, "not authorized")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.UserId)
	if err != nil {
		return nil, status.Error(codes.Internal, "not found")
	}
	return user.ToAPI(), nil
}

func (s *userManagementServer) ChangePassword(ctx context.Context, req *api.PasswordChangeMsg) (*api.ServiceStatus, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if !utils.CheckPasswordFormat(req.NewPassword) {
		return nil, status.Error(codes.InvalidArgument, "new password too weak")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user and/or password")
	}

	match, err := pwhash.ComparePasswordWithHash(user.Account.Password, req.OldPassword)
	if err != nil || !match {
		s.SaveLogEvent(req.Token.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_AUTH_WRONG_PASSWORD, "change password endpoint")
		return nil, status.Error(codes.InvalidArgument, "invalid user and/or password")
	}

	newHashedPw, err := pwhash.HashPassword(req.NewPassword)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	err = s.userDBservice.UpdateUserPassword(req.Token.InstanceId, req.Token.Id, newHashedPw)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	logger.Info.Printf("user %s initiated password change", req.Token.Id)

	// Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:        req.Token.InstanceId,
		To:                []string{user.Account.AccountID},
		MessageType:       constants.EMAIL_TYPE_PASSWORD_CHANGED,
		PreferredLanguage: user.Account.PreferredLanguage,
		UseLowPrio:        true,
	})
	if err != nil {
		logger.Error.Printf("ChangePassword: %s", err.Error())
	}
	// ---

	// remove all temptokens for password reset:
	if err := s.globalDBService.DeleteAllTempTokenForUser(req.Token.InstanceId, req.Token.Id, constants.TOKEN_PURPOSE_PASSWORD_RESET); err != nil {
		logger.Error.Printf("ChangePassword: %s", err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_PASSWORD_CHANGED, "")

	return &api.ServiceStatus{
		Status: api.ServiceStatus_NORMAL,
		Msg:    "password changed",
	}, nil
}

func (s *userManagementServer) ChangeAccountIDEmail(ctx context.Context, req *api.EmailChangeMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.NewEmail == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	req.NewEmail = utils.SanitizeEmail(req.NewEmail)
	if !utils.CheckEmailFormat(req.NewEmail) {
		return nil, status.Error(codes.InvalidArgument, "email not valid")
	}
	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	match, err := pwhash.ComparePasswordWithHash(user.Account.Password, req.Password)
	if err != nil || !match {
		s.SaveLogEvent(req.Token.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_AUTH_WRONG_PASSWORD, "change account id endpoint")
		return nil, status.Error(codes.InvalidArgument, "action failed")
	}

	// is email address still free to use?
	_, err = s.userDBservice.GetUserByAccountID(req.Token.InstanceId, req.NewEmail)
	if err == nil {
		return nil, status.Error(codes.Internal, "action failed")
	}

	if user.Account.Type != models.ACCOUNT_TYPE_EMAIL {
		return nil, status.Error(codes.Internal, "account is not email type")
	}
	oldCI, oldFound := user.FindContactInfoByTypeAndAddr("email", user.Account.AccountID)
	if !oldFound {
		return nil, status.Error(codes.Internal, "old contact info not found - unexpected error")
	}

	if user.Account.AccountConfirmedAt > 0 {
		// Old AccountID already confirmed

		// TempToken for contact verification:
		tempTokenInfos := models.TempToken{
			UserID:     user.ID.Hex(),
			InstanceID: req.Token.InstanceId,
			Purpose:    constants.TOKEN_PURPOSE_RESTORE_ACCOUNT_ID,
			Info: map[string]string{
				"oldEmail": user.Account.AccountID,
				"newEmail": req.NewEmail,
			},
			Expiration: tokens.GetExpirationTime(time.Hour * 24 * 7),
		}
		tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		// ---> Trigger message sending
		_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
			InstanceId:        req.Token.InstanceId,
			To:                []string{user.Account.AccountID},
			MessageType:       constants.EMAIL_TYPE_ACCOUNT_ID_CHANGED,
			PreferredLanguage: user.Account.PreferredLanguage,
			ContentInfos: map[string]string{
				"restoreToken": tempToken,
				"validUntil":   strconv.Itoa(24 * 7 * 60),
				"newEmail":     req.NewEmail,
			},
			UseLowPrio: true,
		})
		if err != nil {
			logger.Error.Printf("ChangeAccountIDEmail: %s", err.Error())
		}
		// <---
	}
	// if old AccountID was not confirmed probably wrong address used in the first place
	if user.Profiles[0].Alias == user.Account.AccountID {
		user.Profiles[0].Alias = req.NewEmail
	}
	user.Account.AccountID = req.NewEmail
	user.Account.AccountConfirmedAt = -1

	// Add new address to contact list if necessary:
	ci, found := user.FindContactInfoByTypeAndAddr("email", req.NewEmail)
	if found {
		// new email already confirmed
		if ci.ConfirmedAt > 0 {
			user.Account.AccountConfirmedAt = ci.ConfirmedAt
		}
	} else {
		user.AddNewEmail(req.NewEmail, false)
	}

	newCI, newFound := user.FindContactInfoByTypeAndAddr("email", req.NewEmail)
	if !newFound {
		return nil, status.Error(codes.Internal, "new contact info not found - unexpected error")
	}
	user.ReplaceContactInfoInContactPreferences(oldCI.ID.Hex(), newCI.ID.Hex())

	// start confirmation workflow of necessary:
	if user.Account.AccountConfirmedAt <= 0 {
		// TempToken for contact verification:
		tempTokenInfos := models.TempToken{
			UserID:     user.ID.Hex(),
			InstanceID: req.Token.InstanceId,
			Purpose:    constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
			Info: map[string]string{
				"type":  "email",
				"email": user.Account.AccountID,
			},
			Expiration: tokens.GetExpirationTime(time.Hour * 24 * 30),
		}
		tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		// ---> Trigger message sending
		_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
			InstanceId:        req.Token.InstanceId,
			To:                []string{user.Account.AccountID},
			MessageType:       constants.EMAIL_TYPE_VERIFY_EMAIL,
			PreferredLanguage: user.Account.PreferredLanguage,
			ContentInfos: map[string]string{
				"token": tempToken,
			},
		})
		if err != nil {
			logger.Error.Printf("ChangeAccountIDEmail: %s", err.Error())
		}
		// <---
	}

	if !req.KeepOldEmail {
		err := user.RemoveContactInfo(oldCI.ID.Hex())
		if err != nil {
			logger.Error.Println(err.Error())
		}
	}

	// Save user:
	updUser, err := s.userDBservice.UpdateUser(req.Token.InstanceId, user)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, updUser.ID.Hex(), loggingAPI.LogEventType_LOG, constants.LOG_EVENT_ACCOUNT_ID_CHANGED, updUser.Account.AccountID)

	return updUser.ToAPI(), nil
}

func (s *userManagementServer) DeleteAccount(ctx context.Context, req *api.UserReference) (*api.ServiceStatus, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	// TODO: check if user auth is from admin - to remove user by admin
	if req.Token.Id != req.UserId {
		logger.Warning.Printf("unauthorized request: user %s initiated account removal for user id %s", req.Token.Id, req.UserId)
		return nil, status.Error(codes.PermissionDenied, "not authorized")
	}
	logger.Info.Printf("user %s initiated account removal for user id %s", req.Token.Id, req.UserId)

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.UserId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// ---> Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:        req.Token.InstanceId,
		To:                []string{user.Account.AccountID},
		MessageType:       constants.EMAIL_TYPE_ACCOUNT_DELETED,
		PreferredLanguage: user.Account.PreferredLanguage,
		UseLowPrio:        true,
	})
	if err != nil {
		logger.Error.Printf("DeleteAccount: %s", err.Error())
	}
	// <---

	if err := s.userDBservice.DeleteUser(req.Token.InstanceId, req.UserId); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// remove all TempTokens for the given user ID using auth-service
	if err := s.globalDBService.DeleteAllTempTokenForUser(req.Token.InstanceId, req.Token.Id, ""); err != nil {
		logger.Error.Printf("error, when trying to remove temp-tokens: %s", err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_ACCOUNT_DELETED, user.Account.AccountID)

	logger.Info.Printf("user account with id %s successfully removed", req.UserId)
	return &api.ServiceStatus{
		Status: api.ServiceStatus_NORMAL,
		Msg:    "user deleted",
	}, nil
}

func (s *userManagementServer) ChangePreferredLanguage(ctx context.Context, req *api.LanguageChangeMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.LanguageCode == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	user, err := s.userDBservice.UpdateAccountPreferredLang(req.Token.InstanceId, req.Token.Id, req.LanguageCode)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return user.ToAPI(), nil
}

func (s *userManagementServer) SaveProfile(ctx context.Context, req *api.ProfileRequest) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.Profile == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	if req.Profile.Id == "" {
		if len(user.Profiles) > maximumProfilesAllowed {
			s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_PROFILE_SAVED, "too many profiles added"+req.Profile.Alias)
			return nil, status.Error(codes.Internal, "reached profile limit")
		}
		user.AddProfile(models.ProfileFromAPI(req.Profile))
	} else {
		err := user.UpdateProfile(models.ProfileFromAPI(req.Profile))
		if err != nil {
			return nil, status.Error(codes.Internal, "profile not found")
		}
	}

	updUser, err := s.userDBservice.UpdateUser(req.Token.InstanceId, user)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_PROFILE_SAVED, req.Profile.Alias)

	return updUser.ToAPI(), nil
}

func (s *userManagementServer) RemoveProfile(ctx context.Context, req *api.ProfileRequest) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.Profile == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	if len(user.Profiles) == 1 {
		return nil, status.Error(codes.Internal, "can't delete last profile")
	}

	if err := user.RemoveProfile(req.Profile.Id); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	updUser, err := s.userDBservice.UpdateUser(req.Token.InstanceId, user)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_PROFILE_REMOVED, "id: "+req.Profile.Id)
	return updUser.ToAPI(), nil
}

func (s *userManagementServer) UpdateContactPreferences(ctx context.Context, req *api.ContactPreferencesMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.ContactPreferences == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	user, err := s.userDBservice.UpdateContactPreferences(req.Token.InstanceId, req.Token.Id, models.ContactPreferencesFromAPI(req.ContactPreferences))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return user.ToAPI(), nil
}

func (s *userManagementServer) UseUnsubscribeToken(ctx context.Context, req *api.TempToken) (*api.ServiceStatus, error) {
	if req == nil || req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	tokenInfos, err := s.ValidateTempToken(req.Token, []string{constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER})
	if err != nil {
		logger.Error.Printf("UseUnsubscribeToken: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	user, err := s.userDBservice.GetUserByID(tokenInfos.InstanceID, tokenInfos.UserID)
	if err != nil {
		logger.Error.Printf("UseUnsubscribeToken: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	user.ContactPreferences.SubscribedToNewsletter = false

	_, err = s.userDBservice.UpdateContactPreferences(tokenInfos.InstanceID, user.ID.Hex(), user.ContactPreferences)
	if err != nil {
		logger.Error.Printf("UseUnsubscribeToken: %s", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &api.ServiceStatus{
		Status: api.ServiceStatus_NORMAL,
		Msg:    "unsubscribed",
	}, nil
}

func (s *userManagementServer) AddEmail(ctx context.Context, req *api.ContactInfoMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.ContactInfo == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if req.ContactInfo.Type != "email" {
		return nil, status.Error(codes.InvalidArgument, "wrong contact type")
	}

	email := utils.SanitizeEmail(req.ContactInfo.GetEmail())
	if !utils.CheckEmailFormat(email) {
		return nil, status.Error(codes.InvalidArgument, "email not valid")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	user.AddNewEmail(email, false)

	// TempToken for contact verification:
	tempTokenInfos := models.TempToken{
		UserID:     user.ID.Hex(),
		InstanceID: req.Token.InstanceId,
		Purpose:    constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
		Info: map[string]string{
			"type":  "email",
			"email": email,
		},

		Expiration: tokens.GetExpirationTime(time.Hour * 24 * 30),
	}
	tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// ---> Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:  req.Token.InstanceId,
		To:          []string{user.Account.AccountID},
		MessageType: constants.EMAIL_TYPE_VERIFY_EMAIL,
		ContentInfos: map[string]string{
			"token": tempToken,
		},
		PreferredLanguage: user.Account.PreferredLanguage,
	})
	if err != nil {
		logger.Error.Printf("AddEmail: %s", err.Error())
	}
	// <---

	updUser, err := s.userDBservice.UpdateUser(req.Token.InstanceId, user)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return updUser.ToAPI(), nil
}

func (s *userManagementServer) RemoveEmail(ctx context.Context, req *api.ContactInfoMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.ContactInfo == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	err = user.RemoveContactInfo(req.ContactInfo.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	updUser, err := s.userDBservice.UpdateUser(req.Token.InstanceId, user)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return updUser.ToAPI(), nil
}
