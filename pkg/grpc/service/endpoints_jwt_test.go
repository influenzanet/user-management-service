package service

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestValidateJWT(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
	}

	t.Run("without payload", func(t *testing.T) {
		_, err := s.ValidateJWT(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing arguments")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		req := &api.JWTRequest{}
		_, err := s.ValidateJWT(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing arguments")
		if !ok {
			t.Error(msg)
		}
	})

	adminToken, err1 := tokens.GenerateNewToken("test-admin-id", true, "testprofid", []string{"PARTICIPANT", "ADMIN"}, testInstanceID, s.Intervals.TokenExpiryInterval, "", nil, []string{})
	userToken, err2 := tokens.GenerateNewToken(
		"test-user-id",
		true,
		"testprofid",
		[]string{"PARTICIPANT"},
		testInstanceID,
		s.Intervals.TokenExpiryInterval,
		"",
		&models.TempToken{UserID: "test-user-id", Purpose: "testpurpose"},
		[]string{},
	)
	if err1 != nil || err2 != nil {
		t.Errorf("unexpected error: %s or %s", err1, err2)
		return
	}

	t.Run("with wrong token", func(t *testing.T) {
		req := &api.JWTRequest{
			Token: adminToken + "x",
		}

		_, err := s.ValidateJWT(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid token")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with normal user token", func(t *testing.T) {
		req := &api.JWTRequest{
			Token: userToken,
		}

		resp, err := s.ValidateJWT(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		roles := tokens.GetRolesFromPayload(resp.Payload)
		if resp == nil || resp.InstanceId != testInstanceID || resp.Id != "test-user-id" || len(roles) != 1 || roles[0] != "PARTICIPANT" {
			t.Errorf("unexpected response: %s", resp)
			return
		}
		if resp.TempToken == nil || resp.TempToken.Purpose != "testpurpose" {
			t.Errorf("unexpected temptoken in response: %s", resp.TempToken)
			return
		}
	})

	t.Run("with admin token", func(t *testing.T) {
		req := &api.JWTRequest{
			Token: adminToken,
		}

		resp, err := s.ValidateJWT(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		roles := tokens.GetRolesFromPayload(resp.Payload)
		if resp == nil || len(roles) < 2 {
			t.Errorf("unexpected response: %s", resp)
			return
		}
	})

	if testing.Short() {
		t.Skip("skipping waiting for token test in short mode, since it has to wait for token expiration.")
	}
	time.Sleep(s.Intervals.TokenExpiryInterval + time.Second)

	t.Run("with expired token", func(t *testing.T) {
		req := &api.JWTRequest{
			Token: adminToken,
		}
		_, err := s.ValidateJWT(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid token")
		if !ok {
			t.Error(msg)
		}
	})
}

func TestRenewJWT(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)

	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
		clients: &models.APIClients{
			LoggingService: mockLoggingClient,
		},
	}
	refreshToken := "TEST-REFRESH-TOKEN-STRING"
	testUsers, err := addTestUsers([]models.User{
		{
			Account: models.Account{
				Type:      "email",
				AccountID: "test_for_renew_token@test.com",
			},
			Profiles: []models.Profile{
				{
					ID:    primitive.NewObjectID(),
					Alias: "main",
				},
			},
		},
	})
	if err != nil {
		t.Errorf("failed to create testusers: %s", err.Error())
		return
	}

	testUserDBService.CreateRenewToken(testInstanceID, testUsers[0].ID.Hex(), refreshToken, time.Now().Add(time.Hour).Unix())

	userToken, err := tokens.GenerateNewToken(testUsers[0].ID.Hex(), true, "testprofid", []string{"PARTICIPANT"}, testInstanceID, s.Intervals.TokenExpiryInterval, "", nil, []string{})
	if err != nil {
		t.Errorf("unexpected error: %s", err)
		return
	}

	t.Run("Testing token refresh without token", func(t *testing.T) {
		_, err := s.RenewJWT(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing arguments")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty token", func(t *testing.T) {
		req := &api.RefreshJWTRequest{}

		_, err := s.RenewJWT(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing arguments")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong access token", func(t *testing.T) {
		req := &api.RefreshJWTRequest{
			AccessToken:  userToken + "x",
			RefreshToken: refreshToken,
		}
		_, err := s.RenewJWT(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "refresh token error")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong refresh token", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		req := &api.RefreshJWTRequest{
			AccessToken:  userToken,
			RefreshToken: userToken + "x",
		}
		_, err := s.RenewJWT(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "refresh token error")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with normal tokens", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		//test if MarkedForDeletionTime is updated
		succ, err := testUserDBService.UpdateMarkedForDeletionTime(testInstanceID, testUsers[0].ID.Hex(), 100, false)
		if succ != true {
			t.Errorf("could not update markedForDeletion Time")
			return
		}
		req := &api.RefreshJWTRequest{
			AccessToken:  userToken,
			RefreshToken: refreshToken,
		}
		resp, err := s.RenewJWT(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if resp == nil {
			t.Error("response is missing")
			return
		}
		if len(resp.AccessToken) < 10 || len(resp.RefreshToken) < 10 {
			t.Errorf("unexpected response: %s", resp)
			return
		}
		user, err := testUserDBService.GetUserByID(testInstanceID, testUsers[0].ID.Hex())
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if user.Timestamps.MarkedForDeletion != 0 {
			t.Errorf("timestamp MarkedForDeletion should be 0, but it is: %v", user.Timestamps.MarkedForDeletion)
			return
		}
	})

	time.Sleep(s.Intervals.TokenExpiryInterval)

	// Test with expired token
	t.Run("with expired token", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		req := &api.RefreshJWTRequest{
			AccessToken:  userToken,
			RefreshToken: refreshToken,
		}
		resp, err := s.RenewJWT(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if resp == nil {
			t.Error("response is missing")
			return
		}
		if len(resp.AccessToken) < 10 || len(resp.RefreshToken) < 10 {
			t.Errorf("unexpected response: %s", resp)
			return
		}
	})
}

func TestRevokeAllRefreshTokens(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
	}
	refreshToken := "TEST-REFRESH-TOKEN-STRING"
	testUsers, err := addTestUsers([]models.User{
		{
			Account: models.Account{
				Type:      "email",
				AccountID: "test_for_revoke_refresh_tokens@test.com",
			},
			Profiles: []models.Profile{
				{
					ID:    primitive.NewObjectID(),
					Alias: "main",
				},
			},
		},
	})
	if err != nil {
		t.Errorf("failed to create testusers: %s", err.Error())
		return
	}
	testUserDBService.CreateRenewToken(testInstanceID, testUsers[0].ID.Hex(), refreshToken, time.Now().Add(time.Hour).Unix())

	t.Run("Testing token refresh without token", func(t *testing.T) {
		_, err := s.RevokeAllRefreshTokens(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing arguments")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty req", func(t *testing.T) {
		req := &api.RevokeRefreshTokensReq{}

		_, err := s.RevokeAllRefreshTokens(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing arguments")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("revoke", func(t *testing.T) {
		req := &api.RevokeRefreshTokensReq{
			Token: &api_types.TokenInfos{
				InstanceId: testInstanceID,
				Id:         testUsers[0].ID.Hex(),
			},
		}

		_, err := s.RevokeAllRefreshTokens(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		_, err = s.userDBservice.FindAndUpdateRenewToken(testInstanceID, testUsers[0].ID.Hex(), refreshToken, "test")
		if err == nil {
			t.Error("token should be revoked")
			return
		}
	})
}
