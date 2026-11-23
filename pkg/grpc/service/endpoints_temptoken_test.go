package service

import (
	"context"
	"testing"
	"time"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"google.golang.org/grpc/status"
)

func TestGetOrCreateTemptokenEndpoint(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
	}

	testTempToken := models.TempToken{
		UserID:     "test_user_id",
		InstanceID: testInstanceID,
		Purpose:    "test_purpose_get_or_create_token",
		Info: map[string]string{
			"key": "test_info",
		},
		Expiration: tokens.GetExpirationTime(10 * time.Second),
	}
	token, err := testGlobalDBService.AddTempToken(testTempToken)
	if err != nil {
		t.Error(err)
		return
	}
	testTempToken.Token = token

	t.Run("without payload", func(t *testing.T) {
		_, err := s.GetOrCreateTemptoken(context.Background(), nil)
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		_, err := s.GetOrCreateTemptoken(context.Background(), &api_types.TempTokenInfo{})
		ok, msg := shouldHaveGrpcErrorStatus(err, "missing argument")
		if !ok {
			t.Error(msg)
		}
	})

	t.Run("with not existing token", func(t *testing.T) {
		resp, err := s.GetOrCreateTemptoken(context.Background(), &api_types.TempTokenInfo{
			Purpose:    testTempToken.Purpose,
			UserId:     "otheruserhere",
			InstanceId: testInstanceID,
		})
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if len(resp.Token) < 1 {
			t.Error("token should be found.")
			return
		}
	})

	t.Run("with existing token", func(t *testing.T) {
		resp, err := s.GetOrCreateTemptoken(context.Background(), &api_types.TempTokenInfo{
			Purpose:    testTempToken.Purpose,
			UserId:     testTempToken.UserID,
			InstanceId: testInstanceID,
		})
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if resp.Token != testTempToken.Token {
			t.Errorf("unexpected token: %s,  want: %s", resp.Token, testTempToken.Token)
			return
		}
	})

	// The background cleanup leaves tokens expired for less than an hour in place: they must
	// not be handed out, since the link carrying them would not work.
	addToken := func(t *testing.T, userID string, expiration int64) string {
		t.Helper()
		token, err := testGlobalDBService.AddTempToken(models.TempToken{
			UserID: userID, InstanceID: testInstanceID, Purpose: testTempToken.Purpose, Expiration: expiration,
		})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	getOrCreate := func(t *testing.T, userID string, expiration int64) string {
		t.Helper()
		resp, err := s.GetOrCreateTemptoken(context.Background(), &api_types.TempTokenInfo{
			Purpose: testTempToken.Purpose, UserId: userID, InstanceId: testInstanceID, Expiration: expiration,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return resp.Token
	}

	t.Run("with only an expired token: a new one", func(t *testing.T) {
		expired := addToken(t, "user_with_expired_token", time.Now().Unix()-60)
		want := time.Now().Unix() + 600
		got := getOrCreate(t, "user_with_expired_token", want)
		if got == expired {
			t.Fatal("the expired token was handed out")
		}
		info, err := s.ValidateTempToken(got, []string{testTempToken.Purpose})
		if err != nil {
			t.Fatalf("the new token does not validate: %v", err)
		}
		if info.Expiration != want || info.UserID != "user_with_expired_token" {
			t.Errorf("unexpected new token %+v", info)
		}
		if again := getOrCreate(t, "user_with_expired_token", want); again != got {
			t.Errorf("the next call made another token: %s, want %s", again, got)
		}
	})

	t.Run("with an expired and a valid token: the valid one", func(t *testing.T) {
		addToken(t, "user_with_both_tokens", time.Now().Unix()-60)
		valid := addToken(t, "user_with_both_tokens", time.Now().Unix()+600)
		if got := getOrCreate(t, "user_with_both_tokens", time.Now().Unix()+600); got != valid {
			t.Errorf("got %s, want the valid token %s", got, valid)
		}
	})
}

func TestGenerateTempTokenEndpoint(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
	}

	testTempToken := &api_types.TempTokenInfo{
		UserId:     "test_user_id",
		InstanceId: testInstanceID,
		Purpose:    "test_purpose",
		Info: map[string]string{
			"key": "test_info",
		},
	}

	t.Run("without payload", func(t *testing.T) {
		resp, err := s.GenerateTempToken(context.Background(), nil)
		if err == nil {
			t.Errorf("or response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "missing argument" {
			t.Errorf("wrong error: %s", err.Error())
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		resp, err := s.GenerateTempToken(context.Background(), &api_types.TempTokenInfo{})
		if err == nil {
			t.Errorf("or response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "missing argument" {
			t.Errorf("wrong error: %s", err.Error())
		}
	})

	t.Run("with valid TempToken", func(t *testing.T) {
		resp, err := s.GenerateTempToken(context.Background(), testTempToken)
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if resp.Token == "" {
			t.Errorf("wrong response: %s", resp)
		}
	})
}

func TestGetTempTokensEndpoint(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
	}

	testTempToken := models.TempToken{
		UserID:     "test_user_id",
		InstanceID: testInstanceID,
		Purpose:    "test_purpose_get_tokens",
		Info: map[string]string{
			"key": "test_info",
		},
		Expiration: tokens.GetExpirationTime(10 * time.Second),
	}
	token, err := testGlobalDBService.AddTempToken(testTempToken)
	if err != nil {
		t.Error(err)
		return
	}
	testTempToken.Token = token

	t.Run("without payload", func(t *testing.T) {
		resp, err := s.GetTempTokens(context.Background(), nil)
		if err == nil || resp != nil {
			t.Errorf("wrong response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "missing argument" {
			t.Errorf("wrong error: %s", err.Error())
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		resp, err := s.GetTempTokens(context.Background(), &api_types.TempTokenInfo{})
		if err == nil || resp != nil {
			t.Errorf("wrong response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "missing argument" {
			t.Errorf("wrong error: %s", err.Error())
		}
	})

	t.Run("get by user_id + instace_id", func(t *testing.T) {
		resp, err := s.GetTempTokens(context.Background(), &api_types.TempTokenInfo{
			UserId:     testTempToken.UserID,
			InstanceId: testTempToken.InstanceID,
		})
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if len(resp.TempTokens) < 1 {
			t.Error("token should be found.")
			return
		}
	})

	t.Run("get by user_id + instace_id + type", func(t *testing.T) {
		resp, err := s.GetTempTokens(context.Background(), &api_types.TempTokenInfo{
			UserId:     testTempToken.UserID,
			InstanceId: testTempToken.InstanceID,
			Purpose:    testTempToken.Purpose,
		})
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		if len(resp.TempTokens) != 1 {
			t.Error("exactly one token should be found.")
			return
		}
	})
}

func TestDeleteTempTokenEndpoint(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
	}

	testTempToken := models.TempToken{
		UserID:     "test_user_id",
		InstanceID: testInstanceID,
		Purpose:    "test_purpose_delete_token",
		Info: map[string]string{
			"key": "test_info",
		},
		Expiration: tokens.GetExpirationTime(10 * time.Second),
	}
	token, err := testGlobalDBService.AddTempToken(testTempToken)
	if err != nil {
		t.Error(err)
		return
	}
	testTempToken.Token = token

	t.Run("without payload", func(t *testing.T) {
		resp, err := s.DeleteTempToken(context.Background(), nil)
		if err == nil {
			t.Errorf("or response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "missing argument" {
			t.Errorf("wrong error: %s", err.Error())
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		resp, err := s.DeleteTempToken(context.Background(), &api.TempToken{})
		if err == nil || resp != nil {
			t.Errorf("wrong response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "missing argument" {
			t.Errorf("wrong error: %s", err.Error())
		}
	})

	t.Run("with not existing token", func(t *testing.T) {
		resp, err := s.DeleteTempToken(context.Background(), &api.TempToken{
			Token: testTempToken.Token + "1",
		})
		if err == nil || resp != nil {
			t.Errorf("wrong response: %s", resp)
			return
		}
		tt, err := testGlobalDBService.GetTempToken(testTempToken.Token)
		if err != nil || len(tt.Token) < 5 {
			t.Error("token should not be deleted yet")
			return
		}
	})

	t.Run("with existing token", func(t *testing.T) {
		_, err := s.DeleteTempToken(context.Background(), &api.TempToken{
			Token: testTempToken.Token,
		})
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}

		tt, err := testGlobalDBService.GetTempToken(testTempToken.Token)
		if err == nil || len(tt.Token) > 0 {
			t.Error("token should be deleted by now")
			return
		}
	})
}

func TestPurgeUserTempTokensEndpoint(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
	}

	testTempToken := models.TempToken{
		UserID:     "test_user_id",
		InstanceID: testInstanceID,
		Purpose:    "test_purpose_purging",
		Info: map[string]string{
			"key": "test_info",
		},
		Expiration: tokens.GetExpirationTime(10 * time.Second),
	}
	token, err := testGlobalDBService.AddTempToken(testTempToken)
	if err != nil {
		t.Error(err)
		return
	}
	testTempToken.Token = token

	t.Run("without payload", func(t *testing.T) {
		resp, err := s.PurgeUserTempTokens(context.Background(), nil)
		if err == nil || resp != nil {
			t.Errorf("wrong response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "missing argument" {
			t.Errorf("wrong error: %s", err.Error())
		}
	})

	t.Run("with empty payload", func(t *testing.T) {
		resp, err := s.PurgeUserTempTokens(context.Background(), &api_types.TempTokenInfo{})
		if err == nil || resp != nil {
			t.Errorf("wrong response: %s", resp)
			return
		}
		if status.Convert(err).Message() != "missing argument" {
			t.Errorf("wrong error: %s", err.Error())
		}
	})

	t.Run("with not exisiting wrong instance_id", func(t *testing.T) {
		_, err := s.PurgeUserTempTokens(context.Background(), &api_types.TempTokenInfo{
			InstanceId: testTempToken.InstanceID + "1",
			UserId:     testTempToken.UserID,
		})
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		tokens, err := testGlobalDBService.GetTempTokenForUser(testTempToken.InstanceID, testTempToken.UserID, "")
		if err != nil {
			t.Error(err)
			return
		}
		if len(tokens) < 1 {
			t.Error("tokens shouldn't be purged yet")
			return
		}
	})

	t.Run("with not exisiting wrong user_id", func(t *testing.T) {
		_, err := s.PurgeUserTempTokens(context.Background(), &api_types.TempTokenInfo{
			InstanceId: testTempToken.InstanceID,
			UserId:     testTempToken.UserID + "1",
		})
		if err != nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}
		tokens, err := testGlobalDBService.GetTempTokenForUser(testTempToken.InstanceID, testTempToken.UserID, "")
		if err != nil {
			t.Error(err)
			return
		}
		if len(tokens) < 1 {
			t.Error("tokens shouldn't be purged yet")
			return
		}
	})

	t.Run("with exisiting user_id/instance_id combination", func(t *testing.T) {
		resp, err := s.PurgeUserTempTokens(context.Background(), &api_types.TempTokenInfo{
			InstanceId: testTempToken.InstanceID,
			UserId:     testTempToken.UserID,
		})
		if err != nil || resp == nil {
			t.Errorf("unexpected error: %s", err.Error())
			return
		}

		tokens, err := testGlobalDBService.GetTempTokenForUser(testTempToken.InstanceID, testTempToken.UserID, "")
		if err != nil {
			t.Error(err)
			return
		}
		if len(tokens) > 0 {
			t.Error("tokens should be all purged")
			return
		}
	})
}
