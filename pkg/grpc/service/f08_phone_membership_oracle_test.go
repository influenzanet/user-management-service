package service

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/internal/config"
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"

	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// F-08: "this number is already registered" is an answer about a third party.
// Signup gave it to anyone, with no account and nothing spent, and AddPhoneNumber and
// EditPhoneNumber gave it to any logged-in account: feed them a list of numbers and they said
// which ones belong to a participant of an epidemiological study.
//
// The question is no longer asked at all. Signup stores the number unverified, and adding
// or changing a phone sends the code, whoever else carries or has proved the number. Ownership is
// settled at verification, which refuses a number another account has proved: only the person
// who receives the code can get that far, and they learn nothing they did not know.

const (
	// The number another participant already holds. It is seeded verified on purpose: an
	// unverified claim is a separate question (F-09) and must not decide these tests.
	oracleTakenPhone = "+393230000901"
	oracleFreePhone  = "+393230000902"
	oracleOwnPhone   = "+393230000903"
)

// seedPhoneOwner registers the number to another account, verified, so that every uniqueness
// check in the service reports it as taken.
func seedPhoneOwner(t *testing.T, accountID string, phone string) {
	t.Helper()
	_, err := addTestUsers([]models.User{{
		Account: models.Account{Type: "email", AccountID: accountID},
		ContactInfos: []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone, ConfirmedAt: time.Now().Unix()},
		},
	}})
	if err != nil {
		t.Fatalf("failed to seed the owner of the number: %s", err.Error())
	}
}

// captureWarnings collects what the service writes to the warning log while fn runs.
func captureWarnings(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	original := logger.Warning.Writer()
	logger.Warning.SetOutput(io.Writer(&buf))
	defer logger.Warning.SetOutput(original)
	fn()
	return buf.String()
}

func newSignupTestServer(t *testing.T) userManagementServer {
	t.Helper()
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)

	mockMessagingClient := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)
	// The registration e-mail leaves in a goroutine and the log event is best effort, so both
	// are allowed any number of times, including none.
	mockMessagingClient.EXPECT().SendInstantEmail(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mockLoggingClient.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	return userManagementServer{
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
		// A number given at signup is only stored while WhatsApp is enabled.
		whatsApp: config.NewWhatsAppSettings(config.WhatsAppConfig{Enabled: true}),
	}
}

func signupReq(email string, phone string) *api.SignupWithEmailMsg {
	return &api.SignupWithEmailMsg{
		Email:             email,
		Password:          "SuperSecurePassword123!§$",
		InstanceId:        testInstanceID,
		PreferredLanguage: "en",
		Phone:             phone,
	}
}

// verifyWithStoredCode completes the verification the way the participant would: with the code
// the service stored (and sent).
func verifyWithStoredCode(t *testing.T, s *userManagementServer, token *api_types.TokenInfos) error {
	t.Helper()
	user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if user.Account.PhoneVerificationCode.Code == "" {
		t.Fatal("no code was stored for the number")
	}
	_, err = s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: token, Code: user.Account.PhoneVerificationCode.Code})
	return err
}

// requireOwnerUntouched checks that the account holding the number verified still holds it, with
// its claim, whatever the other account did.
func requireOwnerUntouched(t *testing.T, ownerAccountID string, phone string) {
	t.Helper()
	owner, err := testUserDBService.GetUserByAccountID(testInstanceID, ownerAccountID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	ci, found := owner.FindContactInfoByTypeAndAddr(models.ContactTypePhone, phone)
	if !found || ci.ConfirmedAt == 0 {
		t.Errorf("the owner lost the verified number: %+v", owner.ContactInfos)
	}
}

func TestSignupWithEmail_ClaimedPhoneIsStoredUnverified(t *testing.T) {
	s := newSignupTestServer(t)
	seedPhoneOwner(t, "oracle_owner@test.com", oracleTakenPhone)

	for name, phone := range map[string]string{"a number another account has verified": oracleTakenPhone, "a free number": oracleFreePhone} {
		t.Run(name+" creates the account, the number unverified", func(t *testing.T) {
			email := "oracle_signup_" + phone[len(phone)-3:] + "@test.com"
			var resp *api.TokenResponse
			warnings := captureWarnings(t, func() {
				var err error
				resp, err = s.SignupWithEmail(context.Background(), signupReq(email, phone))
				if err != nil {
					t.Fatalf("the signup must succeed: %s", err.Error())
				}
			})
			if len(resp.AccessToken) < 1 || len(resp.RefreshToken) < 1 {
				t.Fatalf("unexpected response: %s", resp)
			}
			// Nothing about the number is flagged: there is no probe to flag any more.
			if strings.Contains(warnings, utils.MaskPhone(phone)) {
				t.Errorf("the signup logged the number as something special: %q", warnings)
			}
			user, err := testUserDBService.GetUserByAccountID(testInstanceID, email)
			if err != nil {
				t.Fatalf("the account was not created: %s", err.Error())
			}
			ci, found := user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, phone)
			if !found {
				t.Fatalf("the number was not stored: %+v", user.ContactInfos)
			}
			if ci.ConfirmedAt != 0 {
				t.Errorf("a number given at signup must stay unverified: %+v", ci)
			}
			if user.ContactPreferences.HasPreferredChannel(models.ChannelWhatsApp) {
				t.Errorf("an unverified number must not enable whatsapp: %v", user.ContactPreferences.PreferredChannels)
			}
		})
	}
	requireOwnerUntouched(t, "oracle_owner@test.com", oracleTakenPhone)

	t.Run("an invalid number is still answered on its own", func(t *testing.T) {
		// A malformed number says nothing about who is registered, so the caller keeps the
		// specific answer that lets the form explain the mistake.
		_, err := s.SignupWithEmail(context.Background(), signupReq("oracle_malformed@test.com", "not-a-number"))
		if ok, msg := shouldHaveGrpcErrorStatus(err, "phone not valid"); !ok {
			t.Error(msg)
		}
	})
}

func TestAddPhoneNumberWithAClaimedNumberSendsTheCode(t *testing.T) {
	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)
	s.Intervals.MaxVerificationAttempts = 3
	seedPhoneOwner(t, "oracle_add_owner@test.com", oracleTakenPhone)
	token := addRateLimitTestUser(t, "oracle_add_prober@test.com", nil)

	t.Run("the code is sent as for any number", func(t *testing.T) {
		resp, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: oracleTakenPhone})
		if err != nil {
			t.Fatalf("adding a number another account holds must send the code: %v", err)
		}
		if mockWhatsApp.count() != 1 {
			t.Errorf("expected one send, got %d", mockWhatsApp.count())
		}
		phone, found := phoneOf(t, token.Id)
		if !found || phone.Phone != oracleTakenPhone || phone.ConfirmedAt != 0 {
			t.Errorf("the number must be stored unverified: %+v", phone)
		}
		for _, ci := range resp.ContactInfos {
			if ci.Type == models.ContactTypePhone && ci.ConfirmedAt != 0 {
				t.Errorf("the response shows the number verified: %+v", ci)
			}
		}
	})

	t.Run("verification refuses the number, and the owner keeps it", func(t *testing.T) {
		err := verifyWithStoredCode(t, &s, &token)
		if ok, msg := shouldHaveGrpcErrorStatus(err, "phone number already taken"); !ok {
			t.Error(msg)
		}
		phone, _ := phoneOf(t, token.Id)
		if phone.ConfirmedAt != 0 {
			t.Errorf("the number was confirmed on the second account: %+v", phone)
		}
		if hasChannel(channelsOf(t, token.Id), models.ChannelWhatsApp) {
			t.Errorf("whatsapp was enabled for a number the account does not hold")
		}
		requireOwnerUntouched(t, "oracle_add_owner@test.com", oracleTakenPhone)
	})
}

func TestEditPhoneNumberToAClaimedNumberSendsTheCode(t *testing.T) {
	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)
	s.Intervals.MaxVerificationAttempts = 3
	seedPhoneOwner(t, "oracle_edit_owner@test.com", oracleTakenPhone)
	// The prober already holds a number of their own, which is what EditPhoneNumber requires.
	token := addVerifyCodeTestUser(t, "oracle_edit_prober@test.com", oracleOwnPhone, models.VerificationCode{}, nil, nil)

	t.Run("the code is sent and the number replaced, unverified", func(t *testing.T) {
		if _, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: oracleTakenPhone}); err != nil {
			t.Fatalf("changing to a number another account holds must send the code: %v", err)
		}
		if mockWhatsApp.count() != 1 {
			t.Errorf("expected one send, got %d", mockWhatsApp.count())
		}
		phone, found := phoneOf(t, token.Id)
		if !found || phone.Phone != oracleTakenPhone || phone.ConfirmedAt != 0 {
			t.Errorf("the number must be replaced, unverified: %+v", phone)
		}
	})

	t.Run("verification refuses the number, and the owner keeps it", func(t *testing.T) {
		err := verifyWithStoredCode(t, &s, &token)
		if ok, msg := shouldHaveGrpcErrorStatus(err, "phone number already taken"); !ok {
			t.Error(msg)
		}
		if phone, _ := phoneOf(t, token.Id); phone.ConfirmedAt != 0 {
			t.Errorf("the number was confirmed on the second account: %+v", phone)
		}
		requireOwnerUntouched(t, "oracle_edit_owner@test.com", oracleTakenPhone)
	})
}
