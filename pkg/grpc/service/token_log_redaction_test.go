package service

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// captureErrorLog collects what the service writes to the error log while fn runs.
func captureErrorLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	original := logger.Error.Writer()
	logger.Error.SetOutput(&buf)
	defer logger.Error.SetOutput(original)
	fn()
	return buf.String()
}

// A login link that fails (unknown, expired or wrong purpose) must not put the
// token in the log: the log names it by its fingerprint only.
func TestAutoValidateTempToken_FailureLogsNoTokenValue(t *testing.T) {
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals:       models.Intervals{VerificationCodeLifetime: 60},
	}
	users, err := addTestUsers([]models.User{{
		Account:  models.Account{Type: "email", AccountID: "token-log-redaction@test.com", AccountConfirmedAt: time.Now().Unix()},
		Profiles: []models.Profile{{ID: primitive.NewObjectID(), MainProfile: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	userID := users[0].ID.Hex()

	addToken := func(t *testing.T, expiration int64, purpose string) string {
		t.Helper()
		tok, err := testGlobalDBService.AddTempToken(models.TempToken{
			InstanceID: testInstanceID,
			UserID:     userID,
			Expiration: expiration,
			Purpose:    purpose,
		})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	unknown, err := tokens.GenerateUniqueTokenString()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name            string
		token           string
		wantFingerprint bool
	}{
		{"unknown token", unknown, true},
		{"expired token", addToken(t, time.Now().Unix()-20, constants.TOKEN_PURPOSE_SURVEY_LOGIN), false},
		{"wrong purpose", addToken(t, time.Now().Unix()+60, constants.TOKEN_PURPOSE_PASSWORD_RESET), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var callErr error
			logged := captureWarnings(t, func() {
				_, callErr = s.AutoValidateTempToken(context.Background(), &api.AutoValidateReq{TempToken: tc.token})
			})
			if callErr == nil {
				t.Fatal("the token must be refused")
			}
			if !strings.Contains(logged, "SECURITY WARNING") {
				t.Fatalf("the refusal must still be logged, got %q", logged)
			}
			if strings.Contains(logged, tc.token) {
				t.Fatalf("the warning contains the token value: %q", logged)
			}
			if tc.wantFingerprint && !strings.Contains(logged, tokens.LogFingerprint(tc.token)) {
				t.Fatalf("the warning must name the token by its fingerprint %s, got %q", tokens.LogFingerprint(tc.token), logged)
			}
		})
	}
}

// A refresh token that does not validate must not reach the error log or the
// audit event: the log names it by its fingerprint only.
func TestRenewJWT_WrongRefreshTokenLogsNoTokenValue(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)

	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals:       models.Intervals{TokenExpiryInterval: time.Minute},
		clients:         &models.APIClients{LoggingService: mockLoggingClient},
	}
	users, err := addTestUsers([]models.User{{
		Account:  models.Account{Type: "email", AccountID: "refresh-log-redaction@test.com"},
		Profiles: []models.Profile{{ID: primitive.NewObjectID(), MainProfile: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	userID := users[0].ID.Hex()
	accessToken, err := tokens.GenerateNewToken(userID, true, users[0].Profiles[0].ID.Hex(), []string{"PARTICIPANT"}, testInstanceID, s.Intervals.TokenExpiryInterval, "", nil, []string{})
	if err != nil {
		t.Fatal(err)
	}
	// A real-format refresh token this user does not hold.
	refreshToken, err := tokens.GenerateUniqueTokenString()
	if err != nil {
		t.Fatal(err)
	}

	var auditMsg string
	mockLoggingClient.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, ev *loggingAPI.NewLogEvent, _ ...interface{}) (*api_types.ServiceStatus, error) {
			auditMsg = ev.Msg
			return nil, nil
		})

	var callErr error
	logged := captureErrorLog(t, func() {
		_, callErr = s.RenewJWT(context.Background(), &api.RefreshJWTRequest{AccessToken: accessToken, RefreshToken: refreshToken})
	})
	if callErr == nil {
		t.Fatal("an unknown refresh token must be refused")
	}
	if strings.Contains(logged, refreshToken) {
		t.Fatalf("the error log contains the refresh token: %q", logged)
	}
	if !strings.Contains(logged, "failed to validate renew token ("+tokens.LogFingerprint(refreshToken)+")") {
		t.Fatalf("the error log must name the token by its fingerprint %s, got %q", tokens.LogFingerprint(refreshToken), logged)
	}
	if strings.Contains(auditMsg, refreshToken) {
		t.Fatalf("the audit event contains the refresh token: %q", auditMsg)
	}
}
