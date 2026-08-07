package service

import (
	"context"
	"testing"
	"time"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Resending a WhatsApp verification code is a paid send, and the endpoints that start one —
// AddPhoneNumber and EditPhoneNumber — refuse it when WhatsApp is not configured, when the
// number is already verified, and, through the gateway middleware, when the account itself has
// not been confirmed. The resend path reaches the same send without any of the three.
//
// The guards belong here rather than on the route because the same RPC is also reachable from
// POST /v1/user/resend-verification-message, which carries no middleware: putting the check in
// the service covers every door, including ones added later. The email branch must stay open to
// an unconfirmed account — resending that verification is how an account gets confirmed.

const guardTestPhone = "+391230000601"

func addResendGuardUser(t *testing.T, accountID string, accountConfirmedAt int64, phoneConfirmedAt int64) *api_types.TokenInfos {
	t.Helper()

	users, err := addTestUsers([]models.User{
		{
			Account: models.Account{
				Type:                      "email",
				AccountID:                 accountID,
				AccountConfirmedAt:        accountConfirmedAt,
				PhoneVerificationAttempts: []int64{},
			},
			ContactInfos: []models.ContactInfo{
				{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
				{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: guardTestPhone, ConfirmedAt: phoneConfirmedAt},
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to create test user: %s", err.Error())
	}
	return &api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}
}

func resendPhone(s userManagementServer, token *api_types.TokenInfos) error {
	_, err := s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
		Token:   token,
		Type:    models.ContactTypePhone,
		Address: guardTestPhone,
	})
	return err
}

// assertNothingSpent checks the two things a refused resend must not have done: hand a message
// to Meta, and take one of the three sends the rate limit allows per window.
func assertNothingSpent(t *testing.T, mock *countingWhatsAppClient, userID string) {
	t.Helper()
	if sends := mock.count(); sends != 0 {
		t.Errorf("a refused resend still sent %d message(s) to Meta", sends)
	}
	user, err := testUserDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if spent := len(user.Account.PhoneVerificationAttempts); spent != 0 {
		t.Errorf("a refused resend spent %d slot(s) of the send budget", spent)
	}
}

func TestResendPhoneVerificationGuards(t *testing.T) {
	confirmed := time.Now().Unix()

	t.Run("refuses when WhatsApp is not configured", func(t *testing.T) {
		mock := &countingWhatsAppClient{}
		s := newRateLimitTestServer(mock)
		s.whatsAppConfig = config.WhatsAppConfig{Enabled: false}
		token := addResendGuardUser(t, "resend_guard_disabled@test.com", confirmed, 0)

		if err := resendPhone(s, token); status.Code(err) != codes.Unavailable {
			t.Errorf("expected Unavailable, got %v", err)
		}
		assertNothingSpent(t, mock, token.Id)
	})

	t.Run("refuses for a number that is already verified", func(t *testing.T) {
		mock := &countingWhatsAppClient{}
		s := newRateLimitTestServer(mock)
		token := addResendGuardUser(t, "resend_guard_verified@test.com", confirmed, confirmed)

		if err := resendPhone(s, token); status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", err)
		}
		assertNothingSpent(t, mock, token.Id)
	})

	t.Run("refuses for an account that is not confirmed", func(t *testing.T) {
		mock := &countingWhatsAppClient{}
		s := newRateLimitTestServer(mock)
		token := addResendGuardUser(t, "resend_guard_unconfirmed@test.com", 0, 0)

		if err := resendPhone(s, token); status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected InvalidArgument, got %v", err)
		}
		assertNothingSpent(t, mock, token.Id)
	})

	t.Run("still sends for a confirmed account with an unverified number", func(t *testing.T) {
		mock := &countingWhatsAppClient{}
		s := newRateLimitTestServer(mock)
		token := addResendGuardUser(t, "resend_guard_ok@test.com", confirmed, 0)

		if err := resendPhone(s, token); err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if sends := mock.count(); sends != 1 {
			t.Errorf("expected exactly one message, got %d", sends)
		}
	})
}
