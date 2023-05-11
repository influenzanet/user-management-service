package service

import (
	"context"
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

func (s *userManagementServer) InitiatePasswordReset(ctx context.Context, req *api.InitiateResetPasswordMsg) (*api.ServiceStatus, error) {
	if req == nil || req.AccountId == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if req.InstanceId == "" {
		req.InstanceId = "default"
	}
	if !s.isInstanceIDAllowed(req.InstanceId) {
		logger.Warning.Printf("InitiatePasswordReset: instance ID not allowed: %s", req.InstanceId)
		return nil, status.Error(codes.InvalidArgument, "invalid instance ID")
	}
	req.AccountId = utils.SanitizeEmail(req.AccountId)

	user, err := s.userDBservice.GetUserByAccountID(req.InstanceId, req.AccountId)
	if err != nil {
		logger.Error.Printf("InitiatePasswordReset: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, "invalid account id")
	}

	if utils.HasMoreAttemptsRecently(user.Account.PasswordResetTriggers, 5, passwordResetAttemptWindow) {
		logger.Warning.Printf("SECURITY WARNING: password reset attempt blocked for email address for %s - too many tries recently", req.AccountId)
		time.Sleep(5 * time.Second)
		return nil, status.Error(codes.InvalidArgument, "account blocked for a while")
	}

	// TempToken for contact verification:
	tempTokenInfos := models.TempToken{
		UserID:     user.ID.Hex(),
		InstanceID: req.InstanceId,
		Purpose:    constants.EMAIL_TYPE_PASSWORD_RESET,
		Info: map[string]string{
			"email": user.Account.AccountID,
		},
		Expiration: tokens.GetExpirationTime(time.Hour * 24),
	}
	tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// ---> Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:  req.InstanceId,
		To:          []string{user.Account.AccountID},
		MessageType: constants.EMAIL_TYPE_PASSWORD_RESET,
		ContentInfos: map[string]string{
			"token":      tempToken,
			"validUntil": "24", // hours
		},
		PreferredLanguage: user.Account.PreferredLanguage,
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	// <---

	if err2 := s.userDBservice.SavePasswordResetTrigger(req.InstanceId, user.ID.Hex()); err != nil {
		logger.Error.Printf("DB ERROR: unexpected error when updating user: %s ", err2.Error())
	}

	// ---> Log Event
	s.SaveLogEvent(req.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_LOG, constants.LOG_EVENT_PASSWORD_RESET_INITIATED, "email sent")

	return &api.ServiceStatus{
		Msg:     "email sending triggered",
		Version: apiVersion,
		Status:  api.ServiceStatus_NORMAL,
	}, nil
}

func (s *userManagementServer) GetInfosForPasswordReset(ctx context.Context, req *api.GetInfosForResetPasswordMsg) (*api.UserInfoForPWReset, error) {
	if req == nil || req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	tokenInfos, err := s.ValidateTempToken(req.Token, []string{
		constants.TOKEN_PURPOSE_PASSWORD_RESET,
		constants.TOKEN_PURPOSE_INVITATION,
	})
	if err != nil {
		logger.Error.Printf("GetInfosForPasswordReset: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, "wrong token")
	}

	user, err := s.userDBservice.GetUserByID(tokenInfos.InstanceID, tokenInfos.UserID)
	if err != nil {
		logger.Error.Printf("GetInfosForPasswordReset: %s", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &api.UserInfoForPWReset{
		AccountId: user.Account.AccountID,
	}, nil
}

func (s *userManagementServer) ResetPassword(ctx context.Context, req *api.ResetPasswordMsg) (*api.ServiceStatus, error) {
	if req == nil || req.Token == "" || req.NewPassword == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	tokenInfos, err := s.ValidateTempToken(req.Token,
		[]string{
			constants.TOKEN_PURPOSE_INVITATION,
			constants.TOKEN_PURPOSE_PASSWORD_RESET,
		})

	if err != nil {
		logger.Error.Printf("GetInfosForPasswordReset: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, "wrong token")
	}

	if !utils.CheckPasswordFormat(req.NewPassword) {
		return nil, status.Error(codes.InvalidArgument, "password too weak")
	}

	password, err := pwhash.HashPassword(req.NewPassword)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	err = s.userDBservice.UpdateUserPassword(tokenInfos.InstanceID, tokenInfos.UserID, password)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	logger.Info.Printf("user %s initiated password change", tokenInfos.UserID)

	user, err := s.userDBservice.GetUserByID(tokenInfos.InstanceID, tokenInfos.UserID)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if tokenInfos.Purpose == constants.TOKEN_PURPOSE_INVITATION {
		newContactPrefs := user.ContactPreferences
		newContactPrefs.SubscribedToNewsletter = true
		newContactPrefs.SubscribedToWeekly = true
		_, err = s.userDBservice.UpdateContactPreferences(tokenInfos.InstanceID, tokenInfos.UserID, newContactPrefs)
		if err != nil {
			logger.Error.Printf("unexpected error when updating contact preferences: %v", err)
		}
	}

	// Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:        tokenInfos.InstanceID,
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
	if err := s.globalDBService.DeleteAllTempTokenForUser(tokenInfos.InstanceID, tokenInfos.UserID, constants.TOKEN_PURPOSE_PASSWORD_RESET); err != nil {
		logger.Error.Printf("ChangePassword: %s", err.Error())
	}

	// ---> Log Event
	s.SaveLogEvent(tokenInfos.InstanceID, user.ID.Hex(), loggingAPI.LogEventType_LOG, constants.LOG_EVENT_PASSWORD_RESET, "new password set after password reset")

	return &api.ServiceStatus{
		Version: apiVersion,
		Msg:     "password changed",
		Status:  api.ServiceStatus_NORMAL,
	}, nil
}
