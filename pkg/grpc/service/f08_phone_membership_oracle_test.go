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
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"

	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// F-08: "this number is already registered" is an answer only the owner of the number is
// entitled to. Signup gave it to anyone, with no account and nothing spent, which turns the
// endpoint into a membership oracle: feed it a list of numbers and it says which ones belong to
// a participant of an epidemiological study. AddPhoneNumber and EditPhoneNumber gave the same
// answer to an authenticated caller before reserving a send slot, so probing was unmetered
// there too and the three-per-window budget never came into play.
//
// The fix is not to hide the answer from the account holder, who needs it, but to take the
// reward away: signup answers exactly what a duplicate account address answers, and the two
// authenticated endpoints charge a probe to the same budget a real send spends.

const (
	// The number another participant already holds. It is seeded verified on purpose: an
	// unverified claim is a separate question (F-09) and must not decide these tests.
	oracleTakenPhone = "+391230000901"
	oracleFreePhone  = "+391230000902"
	oracleOwnPhone   = "+391230000903"
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

// attemptsOf returns how many phone verification sends the user has on record, which is the
// budget a probe must now be charged to.
func attemptsOf(t *testing.T, userID string) int {
	t.Helper()
	user, err := testUserDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	return len(user.Account.PhoneVerificationAttempts)
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

func TestSignupWithEmail_TakenPhoneIsIndistinguishable(t *testing.T) {
	s := newSignupTestServer(t)
	seedPhoneOwner(t, "oracle_owner@test.com", oracleTakenPhone)

	t.Run("a taken number is refused exactly as a duplicate account address is", func(t *testing.T) {
		// The reference answer: sign the same address up twice.
		existing := signupReq("oracle_duplicate@test.com", "")
		if _, err := s.SignupWithEmail(context.Background(), existing); err != nil {
			t.Fatalf("the first signup must succeed: %s", err.Error())
		}
		_, duplicateErr := s.SignupWithEmail(context.Background(), existing)
		if duplicateErr == nil {
			t.Fatal("signing the same address up twice must fail")
		}

		_, takenPhoneErr := s.SignupWithEmail(context.Background(), signupReq("oracle_probe@test.com", oracleTakenPhone))
		if takenPhoneErr == nil {
			t.Fatal("signing up with a number another account holds must fail")
		}

		if status.Code(takenPhoneErr) != status.Code(duplicateErr) {
			t.Errorf("the status code tells a registered number from a registered address: %v vs %v",
				status.Code(takenPhoneErr), status.Code(duplicateErr))
		}
		if status.Convert(takenPhoneErr).Message() != status.Convert(duplicateErr).Message() {
			t.Errorf("the message tells a registered number from a registered address: %q vs %q",
				status.Convert(takenPhoneErr).Message(), status.Convert(duplicateErr).Message())
		}
	})

	t.Run("the refusal is logged with the masked number for the operator", func(t *testing.T) {
		warnings := captureWarnings(t, func() {
			_, _ = s.SignupWithEmail(context.Background(), signupReq("oracle_logged@test.com", oracleTakenPhone))
		})

		if !strings.Contains(warnings, "SECURITY WARNING") {
			t.Errorf("the probe was not flagged as a security event: %q", warnings)
		}
		if !strings.Contains(warnings, utils.MaskPhone(oracleTakenPhone)) {
			t.Errorf("the operator cannot tell which number was probed: %q", warnings)
		}
		if strings.Contains(warnings, oracleTakenPhone) {
			t.Errorf("the full number was written to the log: %q", warnings)
		}
	})

	t.Run("a free number still creates the account", func(t *testing.T) {
		resp, err := s.SignupWithEmail(context.Background(), signupReq("oracle_free@test.com", oracleFreePhone))
		if err != nil {
			t.Fatalf("a signup with an unused number must succeed: %s", err.Error())
		}
		if len(resp.AccessToken) < 1 || len(resp.RefreshToken) < 1 {
			t.Fatalf("unexpected response: %s", resp)
		}

		user, err := testUserDBService.GetUserByAccountID(testInstanceID, "oracle_free@test.com")
		if err != nil {
			t.Fatalf("the account was not created: %s", err.Error())
		}
		ci, found := user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, oracleFreePhone)
		if !found {
			t.Fatalf("the number was not stored: %+v", user.ContactInfos)
		}
		if ci.ConfirmedAt != 0 {
			t.Errorf("a number given at signup must stay unverified: %+v", ci)
		}
	})

	t.Run("an invalid number is still answered on its own", func(t *testing.T) {
		// A malformed number says nothing about who is registered, so the caller keeps the
		// specific answer that lets the form explain the mistake.
		_, err := s.SignupWithEmail(context.Background(), signupReq("oracle_malformed@test.com", "not-a-number"))
		if ok, msg := shouldHaveGrpcErrorStatus(err, "phone not valid"); !ok {
			t.Error(msg)
		}
	})
}

func TestAddPhoneNumberProbeSpendsASlot(t *testing.T) {
	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)
	seedPhoneOwner(t, "oracle_add_owner@test.com", oracleTakenPhone)
	token := addRateLimitTestUser(t, "oracle_add_prober@test.com", nil)

	t.Run("a probe costs the caller one of the sends it could have made", func(t *testing.T) {
		before := attemptsOf(t, token.Id)

		_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: oracleTakenPhone})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", err)
		}

		if after := attemptsOf(t, token.Id); after != before+1 {
			t.Errorf("probing a registered number was free: attempts went from %d to %d", before, after)
		}
		if mockWhatsApp.count() != 0 {
			t.Errorf("a refused probe reached Meta %d times", mockWhatsApp.count())
		}
	})

	t.Run("the budget runs out, so probing cannot continue", func(t *testing.T) {
		// One slot is already gone above; spend the rest and check the next probe is stopped
		// by the rate limit rather than answered.
		for i := attemptsOf(t, token.Id); i < allowedPhoneVerificationAttempts; i++ {
			if _, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: oracleTakenPhone}); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("probe %d: expected InvalidArgument, got %v", i+1, err)
			}
		}

		_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: oracleTakenPhone})
		if status.Code(err) != codes.ResourceExhausted {
			t.Errorf("the %dth probe was still answered: %v", allowedPhoneVerificationAttempts+1, err)
		}
	})
}

func TestEditPhoneNumberProbeSpendsASlot(t *testing.T) {
	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)
	seedPhoneOwner(t, "oracle_edit_owner@test.com", oracleTakenPhone)
	// The prober already holds a number of their own, which is what EditPhoneNumber requires.
	token := addVerifyCodeTestUser(t, "oracle_edit_prober@test.com", oracleOwnPhone, models.VerificationCode{}, nil, nil)

	t.Run("a probe costs the caller one of the sends it could have made", func(t *testing.T) {
		before := attemptsOf(t, token.Id)

		_, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: oracleTakenPhone})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected InvalidArgument, got %v", err)
		}

		if after := attemptsOf(t, token.Id); after != before+1 {
			t.Errorf("probing a registered number was free: attempts went from %d to %d", before, after)
		}
		if mockWhatsApp.count() != 0 {
			t.Errorf("a refused probe reached Meta %d times", mockWhatsApp.count())
		}
	})

	t.Run("the refused probe leaves the caller's own number in place", func(t *testing.T) {
		phone, found := phoneOf(t, token.Id)
		if !found || phone.Phone != oracleOwnPhone {
			t.Errorf("the refusal replaced the number it refused to change: %+v", phone)
		}
	})

	t.Run("the budget runs out, so probing cannot continue", func(t *testing.T) {
		for i := attemptsOf(t, token.Id); i < allowedPhoneVerificationAttempts; i++ {
			if _, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: oracleTakenPhone}); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("probe %d: expected InvalidArgument, got %v", i+1, err)
			}
		}

		_, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: oracleTakenPhone})
		if status.Code(err) != codes.ResourceExhausted {
			t.Errorf("the %dth probe was still answered: %v", allowedPhoneVerificationAttempts+1, err)
		}
	})
}
