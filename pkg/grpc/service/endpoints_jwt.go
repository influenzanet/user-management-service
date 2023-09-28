package service

import (
	"context"
	"strings"
	"time"

	"github.com/coneno/logger"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"google.golang.org/grpc/codes"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	"google.golang.org/grpc/status"
)

func (s *userManagementServer) ValidateJWT(ctx context.Context, req *api.JWTRequest) (*api_types.TokenInfos, error) {
	if req == nil || req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}
	// Parse and validate token
	parsedToken, ok, err := tokens.ValidateToken(req.Token)
	if err != nil || !ok {
		return nil, status.Error(codes.InvalidArgument, "invalid token")
	}

	return &api_types.TokenInfos{
		Id:               parsedToken.ID,
		InstanceId:       parsedToken.InstanceID,
		IssuedAt:         parsedToken.IssuedAt,
		AccountConfirmed: parsedToken.AccountConfirmed,
		Payload:          parsedToken.Payload,
		ProfilId:         parsedToken.ProfileID,
		OtherProfileIds:  parsedToken.OtherProfileIDs,
		TempToken:        parsedToken.TempTokenInfos.ToAPI(),
	}, nil
}

func (s *userManagementServer) RenewJWT(ctx context.Context, req *api.RefreshJWTRequest) (*api.TokenResponse, error) {
	if req == nil || req.AccessToken == "" || req.RefreshToken == "" {
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}

	// Parse and validate token
	parsedToken, _, err := tokens.ValidateToken(req.AccessToken)
	if err != nil && !strings.Contains(err.Error(), "token is expired by") {
		logger.Error.Printf("token refresh -> issue with acces token: %v", err.Error())
		return nil, status.Error(codes.PermissionDenied, "refresh token error")
	}

	// Trigger cleanup of expired renew tokens
	go s.userDBservice.DeleteExpiredRenewTokens(parsedToken.InstanceID)

	// Check if user exists
	user, err := s.userDBservice.GetUserByID(parsedToken.InstanceID, parsedToken.ID)
	if err != nil {
		logger.Error.Printf("token refresh -> retrieving user failed with: %v", err.Error())
		return nil, status.Error(codes.Internal, "refresh token error")
	}

	// Generate new refresh token:
	newRefreshToken, err := tokens.GenerateUniqueTokenString()
	if err != nil {
		logger.Error.Printf("token refresh -> cannot generate new refresh token: %v", err.Error())
		return nil, status.Error(codes.Internal, "refresh token error")
	}

	// Check if refresh token is valid
	rt, err := s.userDBservice.FindAndUpdateRenewToken(parsedToken.InstanceID, user.ID.Hex(), req.RefreshToken, newRefreshToken)
	if err != nil {
		logger.Error.Printf("token refresh -> failed to validate renew token: %v", err.Error())
		s.SaveLogEvent(parsedToken.InstanceID, parsedToken.ID, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_TOKEN_REFRESH_FAILED, "wrong refresh token, cannot renew")
		return nil, status.Error(codes.Internal, "refresh token error")
	}

	if rt.NextToken == newRefreshToken {
		// this is the first time the refresh token is used
		err := s.userDBservice.CreateRenewToken(parsedToken.InstanceID, user.ID.Hex(), newRefreshToken, time.Now().Unix()+userdb.RENEW_TOKEN_DEFAULT_LIFETIME)
		if err != nil {
			logger.Error.Printf("token refresh -> failed to create new renew token object: %v", err.Error())
			return nil, status.Error(codes.Internal, "refresh token error")
		}
	} else {
		newRefreshToken = rt.NextToken
	}

	user.Timestamps.LastTokenRefresh = time.Now().Unix()
	roles := tokens.GetRolesFromPayload(parsedToken.Payload)
	username := tokens.GetUsernameFromPayload(parsedToken.Payload)

	mainProfileID, otherProfileIDs := utils.GetMainAndOtherProfiles(user)

	// Generate new access token:
	newToken, err := tokens.GenerateNewToken(parsedToken.ID, user.Account.AccountConfirmedAt > 0, mainProfileID, roles, parsedToken.InstanceID, s.Intervals.TokenExpiryInterval, username, nil, otherProfileIDs)
	if err != nil {
		logger.Error.Printf("renew token error: %v", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	_, err = s.userDBservice.UpdateMarkedForDeletionTime(parsedToken.InstanceID, user.ID.Hex(), 0, true)
	if err != nil {
		logger.Error.Printf("renew token error: %v", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}
	user, err = s.userDBservice.UpdateUser(parsedToken.InstanceID, user)
	if err != nil {
		logger.Error.Printf("renew token error: %v", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(parsedToken.InstanceID, parsedToken.ID, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_TOKEN_REFRESH_SUCCESS, "")

	return &api.TokenResponse{
		AccessToken:       newToken,
		RefreshToken:      newRefreshToken,
		AccountConfirmed:  user.Account.AccountConfirmedAt > 0,
		ExpiresIn:         int32(s.Intervals.TokenExpiryInterval / time.Minute),
		SelectedProfileId: parsedToken.ProfileID,
		Profiles:          user.ToAPI().Profiles,
		PreferredLanguage: user.Account.PreferredLanguage,
	}, nil
}

func (s *userManagementServer) RevokeAllRefreshTokens(ctx context.Context, req *api.RevokeRefreshTokensReq) (*api.ServiceStatus, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) {
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}

	_, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	count, err := s.userDBservice.DeleteRenewTokensForUser(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to delete tokens")
	}
	logger.Debug.Printf("deleted %d renew tokens for user %s", count, req.Token.Id)

	return &api.ServiceStatus{
		Status:  api.ServiceStatus_NORMAL,
		Msg:     "refresh tokens revoked",
		Version: apiVersion,
	}, nil
}
