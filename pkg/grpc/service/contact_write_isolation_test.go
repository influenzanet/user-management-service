package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc"
)

// The phone verification attempts ledger is the only cost control on the WhatsApp channel and
// is maintained by a single atomic operation, ReservePhoneVerificationSlot. Endpoints that
// persist a user document they read earlier undo that guarantee: their snapshot carries a stale
// copy of the ledger, and writing it back silently drops the slots reserved in between.
//
// The tests below pin the invariant these endpoints must respect: a slot reserved while they are
// in flight is still on record once they return. They fail on a full document replace and pass on
// targeted updates, so they are the regression guard for the whole class, not for one endpoint.

// addContactWriteTestUser creates a user with a confirmed email contact info, an unverified phone
// and an empty attempts ledger, and returns a token for it.
func addContactWriteTestUser(t *testing.T, accountID string, phone string, code models.VerificationCode) api_types.TokenInfos {
	t.Helper()

	users, err := addTestUsers([]models.User{
		{
			Account: models.Account{
				Type:                      "email",
				AccountID:                 accountID,
				PhoneVerificationCode:     code,
				PhoneVerificationAttempts: []int64{},
			},
			ContactInfos: []models.ContactInfo{
				{
					ID:          primitive.NewObjectID(),
					Type:        models.ContactTypeEmail,
					Email:       accountID,
					ConfirmedAt: time.Now().Unix(),
				},
				{
					ID:    primitive.NewObjectID(),
					Type:  models.ContactTypePhone,
					Phone: phone,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("failed to create test user: %s", err.Error())
	}

	return api_types.TokenInfos{
		Id:         users[0].ID.Hex(),
		InstanceId: testInstanceID,
	}
}

func recordedAttempts(t *testing.T, userID string) []int64 {
	t.Helper()
	user, err := testUserDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	return user.Account.PhoneVerificationAttempts
}

// TestResendEmailVerificationDoesNotClobberPhoneLedger drives the interleaving deterministically:
// the slot is reserved from inside the outgoing email call, that is after the endpoint has read
// the user and before it persists it, which is exactly the window a concurrent phone send falls
// into in production. The email resend has no business writing the ledger at all.
func TestResendEmailVerificationDoesNotClobberPhoneLedger(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	mockMessagingClient := messageMock.NewMockMessagingServiceApiClient(mockCtrl)

	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		instanceIDs:     []string{testInstanceID},
		Intervals: models.Intervals{
			TokenExpiryInterval:              time.Second * 2,
			VerificationCodeLifetime:         60,
			ContactVerificationTokenLifetime: 60,
		},
		clients: &models.APIClients{
			MessagingService: mockMessagingClient,
		},
	}

	accountID := "resend_email_ledger@test.com"
	token := addContactWriteTestUser(t, accountID, "+391230000101", models.VerificationCode{})

	var reserved bool
	var reserveErr error
	mockMessagingClient.EXPECT().SendInstantEmail(gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, in *messageAPI.SendEmailReq, opts ...grpc.CallOption) (*messageAPI.ServiceStatus, error) {
			// The endpoint is suspended here holding the snapshot it read before this call.
			reserved, reserveErr = testUserDBService.ReservePhoneVerificationSlot(
				testInstanceID, token.Id, allowedPhoneVerificationAttempts, phoneVerificationRateLimitWindow,
			)
			return &messageAPI.ServiceStatus{Status: messageAPI.ServiceStatus_NORMAL}, nil
		})

	if _, err := s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
		Token:   &token,
		Type:    models.ContactTypeEmail,
		Address: accountID,
	}); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}

	if reserveErr != nil {
		t.Fatalf("unexpected error while reserving the slot: %s", reserveErr.Error())
	}
	if !reserved {
		t.Fatal("the slot should have been free: the test user starts with an empty ledger")
	}

	if attempts := recordedAttempts(t, token.Id); len(attempts) != 1 {
		t.Errorf(
			"the email resend dropped a concurrently reserved slot: %d attempts on record instead of 1 (%v)",
			len(attempts), attempts,
		)
	}
}

// TestVerifyWhatsAppCodeDoesNotClobberPhoneLedger races a verification against a slot reservation.
// Verifying a code sends nothing, so it has no attempt to account for and must leave the ledger
// alone: whichever way the two interleave, the reserved slot must still be on record.
//
// The rounds are needed because the endpoint offers no seam to suspend it at: each round is an
// independent user, and a single lost slot across all of them fails the test.
func TestVerifyWhatsAppCodeDoesNotClobberPhoneLedger(t *testing.T) {
	const rounds = 30

	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3

	lost := 0
	for i := 0; i < rounds; i++ {
		code := models.VerificationCode{
			Code:      "123456",
			Attempts:  0,
			CreatedAt: time.Now().Unix(),
			ExpiresAt: time.Now().Unix() + 60,
		}
		token := addContactWriteTestUser(t, fmt.Sprintf("verify_ledger_%02d@test.com", i), fmt.Sprintf("+39123000%03d", 200+i), code)

		var reserved bool
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _ = s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{
				Token: &token,
				Code:  "123456",
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			reserved, _ = testUserDBService.ReservePhoneVerificationSlot(
				testInstanceID, token.Id, allowedPhoneVerificationAttempts, phoneVerificationRateLimitWindow,
			)
		}()
		close(start)
		wg.Wait()

		if reserved && len(recordedAttempts(t, token.Id)) == 0 {
			lost++
		}
	}

	if lost > 0 {
		t.Errorf(
			"VerifyWhatsAppCode dropped a concurrently reserved slot in %d of %d rounds: a verification sends no message and must not write the attempts ledger",
			lost, rounds,
		)
	}
}

// TestVerifyWhatsAppCodeAtMaxAttemptsDoesNotClobberPhoneLedger covers the punitive branch, which
// removes the phone after too many wrong codes: it is driven by submitting wrong codes, so it is
// the branch an attacker can trigger at will.
// The reservation is staggered because this branch reaches its save after three round trips:
// the delay aims at the window between the read and the write. It only affects how reliably the
// old behaviour is caught — targeted updates cannot lose a slot at any timing.
func TestVerifyWhatsAppCodeAtMaxAttemptsDoesNotClobberPhoneLedger(t *testing.T) {
	const rounds = 30
	const stagger = 500 * time.Microsecond

	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 1

	lost := 0
	for i := 0; i < rounds; i++ {
		code := models.VerificationCode{
			Code:      "123456",
			Attempts:  1, // already at the cap: the next attempt takes the remove-phone branch
			CreatedAt: time.Now().Unix(),
			ExpiresAt: time.Now().Unix() + 60,
		}
		token := addContactWriteTestUser(t, fmt.Sprintf("verify_max_ledger_%02d@test.com", i), fmt.Sprintf("+39123000%03d", 300+i), code)

		var reserved bool
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, _ = s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{
				Token: &token,
				Code:  "000000",
			})
		}()
		go func() {
			defer wg.Done()
			<-start
			time.Sleep(stagger)
			reserved, _ = testUserDBService.ReservePhoneVerificationSlot(
				testInstanceID, token.Id, allowedPhoneVerificationAttempts, phoneVerificationRateLimitWindow,
			)
		}()
		close(start)
		wg.Wait()

		if reserved && len(recordedAttempts(t, token.Id)) == 0 {
			lost++
		}
	}

	if lost > 0 {
		t.Errorf(
			"the remove-phone branch of VerifyWhatsAppCode dropped a concurrently reserved slot in %d of %d rounds",
			lost, rounds,
		)
	}
}
