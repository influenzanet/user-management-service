package service

import (
	"context"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/pwhash"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/status"
)

func TestSendVerificationCode(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockMessagingClient := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)

	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		instanceIDs:     []string{testInstanceID},
		clients: &models.APIClients{
			MessagingService: mockMessagingClient,
			LoggingService:   mockLoggingClient,
		},
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
	}

	// Create Test User
	currentPw := "SuperSecurePassword123!§$"
	hashedPw, err := pwhash.HashPassword(currentPw)
	if err != nil {
		t.Errorf("error creating user for testing login")
		return
	}

	testUser := models.User{
		Account: models.Account{
			Type:               "email",
			AccountID:          "test-send-verification-code@test.com",
			AccountConfirmedAt: time.Now().Unix(),
			AuthType:           "2FA",
			Password:           hashedPw,
			PreferredLanguage:  "de",
		},
		Roles: []string{"PARTICIPANT"},
		Profiles: []models.Profile{
			{ID: primitive.NewObjectID()},
		},
	}

	_, err = testUserDBService.AddUser(testInstanceID, testUser)
	if err != nil {
		t.Errorf("unexpected error while creating user: %v", err)
		return
	}

	t.Run("without payload", func(t *testing.T) {
		_, err := s.SendVerificationCode(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid username and/or password")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		_, err := s.SendVerificationCode(context.Background(), &api.SendVerificationCodeReq{})
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid username and/or password")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong password payload", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)
		_, err := s.SendVerificationCode(context.Background(), &api.SendVerificationCodeReq{
			InstanceId: testInstanceID,
			Email:      "test-send-verification-code@test.com",
			Password:   "something-that-is-obivously-wrong",
		})
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid username and/or password")
		if !ok {
			t.Error(msg)
		}
	})

	/*
		t.Run("with valid payload", func(t *testing.T) {
			_, err := s.SendVerificationCode(context.Background(), &api.SendVerificationCodeReq{
				InstanceId: testInstanceID,
				Email:      "test-send-verification-code@test.com",
				Password:   "SuperSecurePassword123!§$",
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
		})
	*/
}

func TestAutoValidateTempToken(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
	}

	// Create Test User
	currentPw := "SuperSecurePassword123!§$"
	hashedPw, err := pwhash.HashPassword(currentPw)
	if err != nil {
		t.Errorf("error creating user for testing login")
		return
	}

	testUser := models.User{
		Account: models.Account{
			Type:               "email",
			AccountID:          "test-autovalidate@test.com",
			AccountConfirmedAt: time.Now().Unix(),
			Password:           hashedPw,
			PreferredLanguage:  "de",
		},
		Roles: []string{"PARTICIPANT"},
		Profiles: []models.Profile{
			{ID: primitive.NewObjectID()},
		},
	}

	id, err := testUserDBService.AddUser(testInstanceID, testUser)
	if err != nil {
		t.Errorf("error creating user for testing login")
		return
	}
	testUser.ID, err = primitive.ObjectIDFromHex(id)
	if err != nil {
		t.Errorf("error converting id")
		return
	}

	// add temp token with correct purpose not expired
	token1, err := s.globalDBService.AddTempToken(models.TempToken{
		InstanceID: testInstanceID,
		UserID:     testUser.ID.Hex(),
		Expiration: time.Now().Unix() + 20,
		Purpose:    constants.TOKEN_PURPOSE_SURVEY_LOGIN,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	// add temp token with correct purpose expired
	token2, err := s.globalDBService.AddTempToken(models.TempToken{
		InstanceID: testInstanceID,
		UserID:     testUser.ID.Hex(),
		Expiration: time.Now().Unix() - 20,
		Purpose:    constants.TOKEN_PURPOSE_SURVEY_LOGIN,
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	// add temp token with wrong purpose not expired
	token3, err := s.globalDBService.AddTempToken(models.TempToken{
		InstanceID: testInstanceID,
		UserID:     testUser.ID.Hex(),
		Expiration: time.Now().Unix() + 20,
		Purpose:    "wrong",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return
	}

	t.Run("without payload", func(t *testing.T) {
		_, err := s.AutoValidateTempToken(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid token")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		_, err := s.AutoValidateTempToken(context.Background(), &api.AutoValidateReq{})
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid token")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with expired token", func(t *testing.T) {
		_, err := s.AutoValidateTempToken(context.Background(), &api.AutoValidateReq{
			TempToken: token2,
		})
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid token")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong token", func(t *testing.T) {
		_, err := s.AutoValidateTempToken(context.Background(), &api.AutoValidateReq{
			TempToken: "wrong token here",
		})
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid token")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong purpose token", func(t *testing.T) {
		_, err := s.AutoValidateTempToken(context.Background(), &api.AutoValidateReq{
			TempToken: token3,
		})
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid token")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("correct temptoken without access token", func(t *testing.T) {
		resp, err := s.AutoValidateTempToken(context.Background(), &api.AutoValidateReq{
			TempToken: token1,
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(resp.VerificationCode) != 6 {
			t.Errorf("unexpected verification code: %s", resp.VerificationCode)
		}
		if resp.IsSameUser {
			t.Error("should be false")
		}
		if resp.AccountId != testUser.Account.AccountID {
			t.Errorf("unexpected account id: %s", resp.AccountId)
		}
	})

	t.Run("correct temptoken with access token same user", func(t *testing.T) {
		accessToken, err := tokens.GenerateNewToken(
			testUser.ID.Hex(), true, "profid", []string{}, testInstanceID, s.Intervals.TokenExpiryInterval, "", nil, []string{},
		)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		resp, err := s.AutoValidateTempToken(context.Background(), &api.AutoValidateReq{
			TempToken:   token1,
			AccessToken: accessToken,
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(resp.VerificationCode) != 6 {
			t.Errorf("unexpected verification code: %s", resp.VerificationCode)
		}
		if !resp.IsSameUser {
			t.Error("should be true")
		}
		if resp.AccountId != testUser.Account.AccountID {
			t.Errorf("unexpected account id: %s", resp.AccountId)
		}
	})

	t.Run("correct temptoken with access token different user", func(t *testing.T) {
		accessToken, err := tokens.GenerateNewToken(
			"different", true, "profid", []string{}, testInstanceID, s.Intervals.TokenExpiryInterval, "", nil, []string{},
		)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		resp, err := s.AutoValidateTempToken(context.Background(), &api.AutoValidateReq{
			TempToken:   token1,
			AccessToken: accessToken,
		})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
			return
		}
		if len(resp.VerificationCode) != 6 {
			t.Errorf("unexpected verification code: %s", resp.VerificationCode)
		}
		if resp.IsSameUser {
			t.Error("should be false")
		}
		if resp.AccountId != testUser.Account.AccountID {
			t.Errorf("unexpected account id: %s", resp.AccountId)
		}
	})
}

func TestLogin(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)

	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		instanceIDs:     []string{testInstanceID},
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
		clients: &models.APIClients{
			LoggingService: mockLoggingClient,
		},
	}

	// Create Test User
	currentPw := "SuperSecurePassword123!§$"
	hashedPw, err := pwhash.HashPassword(currentPw)
	if err != nil {
		t.Errorf("error creating user for testing login")
		return
	}

	testUser1 := models.User{
		Account: models.Account{
			Type:               "email",
			AccountID:          "test-login@test.com",
			AccountConfirmedAt: time.Now().Unix(),
			Password:           hashedPw,
			PreferredLanguage:  "de",
		},
		Roles: []string{"PARTICIPANT"},
		Profiles: []models.Profile{
			{ID: primitive.NewObjectID()},
		},
	}

	id, err := testUserDBService.AddUser(testInstanceID, testUser1)
	if err != nil {
		t.Errorf("error creating user for testing login")
		return
	}
	testUser1.ID, err = primitive.ObjectIDFromHex(id)
	if err != nil {
		t.Errorf("error converting id")
		return
	}

	testUser2 := models.User{
		Account: models.Account{
			Type:               "email",
			AccountID:          "test-login-2fa@test.com",
			AccountConfirmedAt: time.Now().Unix(),
			AuthType:           "2FA",
			VerificationCode: models.VerificationCode{
				Code:      "456345",
				ExpiresAt: time.Now().Unix() + 15,
			},
			Password:          hashedPw,
			PreferredLanguage: "de",
		},
		Roles: []string{"PARTICIPANT"},
		Profiles: []models.Profile{
			{ID: primitive.NewObjectID()},
		},
	}

	id, err = testUserDBService.AddUser(testInstanceID, testUser2)
	if err != nil {
		t.Errorf("error creating user 2 for testing login")
		return
	}
	testUser2.ID, err = primitive.ObjectIDFromHex(id)
	if err != nil {
		t.Errorf("error converting id")
		return
	}

	t.Run("without payload", func(t *testing.T) {
		resp, err := s.LoginWithEmail(context.Background(), nil)
		st, ok := status.FromError(err)
		if !ok || st == nil || st.Message() != "invalid username and/or password" || resp != nil {
			t.Errorf("wrong error: %s", err.Error())
			t.Errorf("or response: %s", resp)
			return
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		req := &api.LoginWithEmailMsg{}

		resp, err := s.LoginWithEmail(context.Background(), req)
		if err == nil || status.Convert(err).Message() != "invalid username and/or password" || resp != nil {
			t.Errorf("wrong error: %s", err.Error())
			t.Errorf("or response: %s", resp)
			return
		}
	})

	t.Run("with wrong email", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		req := &api.LoginWithEmailMsg{
			Email:      "wrong@test.com",
			Password:   currentPw,
			InstanceId: testInstanceID,
		}

		resp, err := s.LoginWithEmail(context.Background(), req)
		if err == nil || resp != nil {
			t.Errorf("wrong response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "invalid username and/or password" {
			t.Errorf("wrong error: %s", err.Error())
			return
		}
	})

	t.Run("with wrong password", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		req := &api.LoginWithEmailMsg{
			Email:      testUser1.Account.AccountID,
			Password:   currentPw + "w",
			InstanceId: testInstanceID,
		}

		resp, err := s.LoginWithEmail(context.Background(), req)
		if err == nil || resp != nil {
			t.Errorf("wrong response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "invalid username and/or password" {
			t.Errorf("wrong error: %s", err.Error())
			return
		}
	})

	t.Run("with valid fields", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		req := &api.LoginWithEmailMsg{
			Email:         testUser1.Account.AccountID,
			Password:      currentPw,
			InstanceId:    testInstanceID,
			AsParticipant: true,
		}

		resp, err := s.LoginWithEmail(context.Background(), req)

		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if resp == nil || len(resp.Token.AccessToken) < 1 || len(resp.Token.RefreshToken) < 1 {
			t.Errorf("unexpected response: %s", resp)
			return
		}

		if resp.Token.PreferredLanguage != "de" || resp.Token.SelectedProfileId != testUser1.Profiles[0].ID.Hex() {
			t.Errorf("unexpected PreferredLanguage or AccountConfirmed: %s", resp)
			return
		}
	})

	// 2FA tests
	t.Run("with wrong verification code", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		req := &api.LoginWithEmailMsg{
			Email:            testUser2.Account.AccountID,
			Password:         currentPw,
			InstanceId:       testInstanceID,
			AsParticipant:    true,
			VerificationCode: "234855",
		}
		_, err := s.LoginWithEmail(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "wrong verfication code")
		if !ok {
			t.Error(msg)
		}
	})

	_, err = testUserDBService.UpdateUser(testInstanceID, testUser2)
	if err != nil {
		t.Errorf("error updating user 2 for testing login")
		return
	}

	t.Run("with valid fields for 2FA", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		req := &api.LoginWithEmailMsg{
			Email:            testUser2.Account.AccountID,
			Password:         currentPw,
			InstanceId:       testInstanceID,
			VerificationCode: testUser2.Account.VerificationCode.Code,
			AsParticipant:    true,
		}

		resp, err := s.LoginWithEmail(context.Background(), req)

		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if resp == nil || len(resp.Token.AccessToken) < 1 || len(resp.Token.RefreshToken) < 1 {
			t.Errorf("unexpected response: %s", resp)
			return
		}

		if resp.Token.PreferredLanguage != "de" || resp.Token.SelectedProfileId != testUser2.Profiles[0].ID.Hex() {
			t.Errorf("unexpected PreferredLanguage or AccountConfirmed: %s", resp)
			return
		}
	})

	t.Run("with brute force attack", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Times(12).Return(nil, nil)

		req := &api.LoginWithEmailMsg{
			Email:         testUser2.Account.AccountID,
			Password:      currentPw + "w",
			InstanceId:    testInstanceID,
			AsParticipant: true,
		}
		for i := 0; i < 11; i++ {
			_, err := s.LoginWithEmail(context.Background(), req)
			ok, msg := shouldHaveGrpcErrorStatus(err, "invalid username and/or password")
			if !ok {
				t.Error(msg)
			}
		}

		_, err := s.LoginWithEmail(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "invalid username and/or password")
		if !ok {
			t.Error(msg)
			return
		}
	})
}

func TestSignupWithEmail(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockMessagingClient := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)

	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		instanceIDs:     []string{testInstanceID},
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
		clients: &models.APIClients{
			MessagingService: mockMessagingClient,
			LoggingService:   mockLoggingClient,
		},
		newUserCountLimit: 100,
	}

	wrongEmailFormatNewUserReq := &api.SignupWithEmailMsg{
		Email:             "test-signup",
		Password:          "SuperSecurePassword123!§$",
		InstanceId:        testInstanceID,
		PreferredLanguage: "en",
	}

	wrongPasswordFormatNewUserReq := &api.SignupWithEmailMsg{
		Email:             "test-signup@test.com",
		Password:          "short",
		InstanceId:        testInstanceID,
		PreferredLanguage: "en",
	}
	validNewUserReq := &api.SignupWithEmailMsg{
		Email:             "test-signup@test.com",
		Password:          "SuperSecurePassword123!§$",
		InstanceId:        testInstanceID,
		PreferredLanguage: "en",
	}

	t.Run("without payload", func(t *testing.T) {
		_, err := s.SignupWithEmail(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		req := &api.SignupWithEmailMsg{}
		_, err := s.SignupWithEmail(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "email not valid")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong email format", func(t *testing.T) {
		_, err := s.SignupWithEmail(context.Background(), wrongEmailFormatNewUserReq)
		ok, msg := shouldHaveGrpcErrorStatus(err, "email not valid")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong password format", func(t *testing.T) {
		_, err := s.SignupWithEmail(context.Background(), wrongPasswordFormatNewUserReq)
		ok, msg := shouldHaveGrpcErrorStatus(err, "password too weak")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with valid fields", func(t *testing.T) {
		mockMessagingClient.EXPECT().SendInstantEmail(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		resp, err := s.SignupWithEmail(context.Background(), validNewUserReq)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if len(resp.AccessToken) < 1 || len(resp.RefreshToken) < 1 {
			t.Errorf("unexpected response: %s", resp)
			return
		}
		if len(resp.SelectedProfileId) < 1 {
			t.Errorf("unexpected selected profile: %s", resp.SelectedProfileId)
			return
		}
		if len(resp.Profiles) != 1 {
			t.Errorf("unexpected number of profiles: %d", len(resp.Profiles))
			return
		}
	})

	t.Run("with duplicate user (same email)", func(t *testing.T) {
		mockMessagingClient.EXPECT().SendInstantEmail(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		req := &api.SignupWithEmailMsg{
			Email:             "test-signup-1@test.com",
			Password:          "SuperSecurePassword123!§$",
			InstanceId:        testInstanceID,
			PreferredLanguage: "en",
		}
		_, err := s.SignupWithEmail(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}

		// Try to signup again:
		resp, err := s.SignupWithEmail(context.Background(), req)
		if err == nil || resp != nil {
			t.Errorf("should fail, when user exists already, wrong response: %s", resp)
			return
		}
	})
}

func TestVerifyAccountEndpoint(t *testing.T) {
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

	testUsers, err := addTestUsers([]models.User{
		{
			Account: models.Account{
				Type:      "email",
				AccountID: "test_for_verify_contact@test.com",
			},
			Profiles: []models.Profile{
				{
					ID:    primitive.NewObjectID(),
					Alias: "main",
				},
			},
			ContactInfos: []models.ContactInfo{
				{
					Type:  "email",
					Email: "test_for_verify_contact@test.com",
				},
				{
					Type:  "email",
					Email: "testadd@test.com",
				},
			},
		},
	})
	if err != nil {
		t.Errorf("failed to create testusers: %s", err.Error())
		return
	}

	t.Run("without payload", func(t *testing.T) {
		_, err := s.VerifyContact(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		req := &api.TempToken{}
		_, err := s.VerifyContact(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong payload", func(t *testing.T) {
		req := &api.TempToken{
			Token: "wrong",
		}
		_, err := s.VerifyContact(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "wrong token")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong token purpose", func(t *testing.T) {
		tempTokenInfos := models.TempToken{
			UserID:     testUsers[0].ID.Hex(),
			InstanceID: testInstanceID,
			Purpose:    "wrong-purpose",
			Info: map[string]string{
				"type":  "email",
				"email": testUsers[0].Account.AccountID,
			},
			Expiration: tokens.GetExpirationTime(time.Hour * 24 * 30),
		}
		tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		req := &api.TempToken{
			Token: tempToken,
		}

		_, err = s.VerifyContact(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "wrong token purpose")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("verify secondary address", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		tempTokenInfos := models.TempToken{
			UserID:     testUsers[0].ID.Hex(),
			InstanceID: testInstanceID,
			Purpose:    constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
			Info: map[string]string{
				"type":  "email",
				"email": testUsers[0].ContactInfos[1].Email,
			},
			Expiration: tokens.GetExpirationTime(time.Hour * 24 * 30),
		}
		tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		req := &api.TempToken{
			Token: tempToken,
		}

		resp, err := s.VerifyContact(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}

		if resp.Account.AccountConfirmedAt > 0 {
			t.Error("account should not be confirmed")
		}
		if len(resp.ContactInfos) != 2 || resp.ContactInfos[1].ConfirmedAt < 1 {
			t.Error("email not confirmed yet")
		}
	})

	t.Run("verify main address", func(t *testing.T) {
		mockLoggingClient.EXPECT().SaveLogEvent(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		tempTokenInfos := models.TempToken{
			UserID:     testUsers[0].ID.Hex(),
			InstanceID: testInstanceID,
			Purpose:    constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
			Info: map[string]string{
				"type":  "email",
				"email": testUsers[0].Account.AccountID,
			},
			Expiration: tokens.GetExpirationTime(time.Hour * 24 * 30),
		}
		tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		req := &api.TempToken{
			Token: tempToken,
		}

		resp, err := s.VerifyContact(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}

		if resp.Account.AccountConfirmedAt < 1 {
			t.Error("account not confirmed yet")
		}
		if len(resp.ContactInfos) != 2 || resp.ContactInfos[0].ConfirmedAt < 1 {
			t.Error("email not confirmed yet")
		}
	})
}

func TestResendContactVerificationEndpoint(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockMessagingClient := messageMock.NewMockMessagingServiceApiClient(mockCtrl)

	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
		clients: &models.APIClients{
			MessagingService: mockMessagingClient,
		},
	}

	testUsers, err := addTestUsers([]models.User{
		{
			Account: models.Account{
				Type:      "email",
				AccountID: "test_for_resend_verify_contact@test.com",
			},
			Profiles: []models.Profile{
				{
					ID:    primitive.NewObjectID(),
					Alias: "main",
				},
			},
			ContactInfos: []models.ContactInfo{
				{
					Type:  "email",
					Email: "test_for_resend_verify_contact@test.com",
				},
			},
		},
	})
	if err != nil {
		t.Errorf("failed to create testusers: %s", err.Error())
		return
	}

	t.Run("without payload", func(t *testing.T) {
		_, err := s.ResendContactVerification(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		req := &api.ResendContactVerificationReq{}
		_, err := s.ResendContactVerification(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with wrong payload", func(t *testing.T) {
		req := &api.ResendContactVerificationReq{
			Token: &api_types.TokenInfos{
				Id:         testUsers[0].ID.Hex(),
				InstanceId: testInstanceID,
			},
			Address: "wrong@wrong.de",
			Type:    "email",
		}
		_, err := s.ResendContactVerification(context.Background(), req)
		ok, msg := shouldHaveGrpcErrorStatus(err, "")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with correct info", func(t *testing.T) {
		mockMessagingClient.EXPECT().SendInstantEmail(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, nil)

		req := &api.ResendContactVerificationReq{
			Token: &api_types.TokenInfos{
				Id:         testUsers[0].ID.Hex(),
				InstanceId: testInstanceID,
			},
			Address: "test_for_resend_verify_contact@test.com",
			Type:    "email",
		}
		_, err := s.ResendContactVerification(context.Background(), req)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
	})
}
