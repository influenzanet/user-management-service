package service

import (
	"context"
	"testing"
	"time"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// F-10: the send budget was counted per account, so it bounded what one account could spend and
// nothing at all of what one phone number could receive. Creating accounts is free, so an attacker
// rotating through them delivered as many WhatsApp verification messages to a victim's number as
// they liked, billed to the platform. F-09 made this cheaper still: a number held unverified by
// somebody else is no longer refused, so every fresh account goes straight through to a send.
//
// These tests drive the three endpoints that pay for a message and assert the number's own budget
// holds across accounts.

// sendsTo reports how many verification codes the mock handed to one destination.
func (m *countingWhatsAppClient) sendsTo(phone string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recipients[phone]
}

// addDestinationTestUser creates a confirmed account, optionally already holding an unverified
// phone number, and returns a token for it. Every account is fresh, which is the point: the
// per-account budget of each one is untouched.
func addDestinationTestUser(t *testing.T, accountID string, phone string) api_types.TokenInfos {
	t.Helper()

	contacts := []models.ContactInfo{
		{
			ID:          primitive.NewObjectID(),
			Type:        models.ContactTypeEmail,
			Email:       accountID,
			ConfirmedAt: time.Now().Unix(),
		},
	}
	if phone != "" {
		contacts = append(contacts, models.ContactInfo{
			ID:    primitive.NewObjectID(),
			Type:  models.ContactTypePhone,
			Phone: phone,
			// Older than the per-contact cooldown, so a resend is not refused for being too soon.
			ConfirmationLinkSentAt: time.Now().Unix() - contactVerificationMessageCooldown - 60,
		})
	}

	users, err := addTestUsers([]models.User{
		{
			Account: models.Account{
				Type:               "email",
				AccountID:          accountID,
				AccountConfirmedAt: time.Now().Unix(),
			},
			ContactInfos: contacts,
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

// exhaustDestinationBudget spends the destination's whole window from fresh accounts, one send
// each, and asserts every one of them went through. It returns the number of messages the mock
// recorded for that number, so the caller can prove the refusal that follows changed nothing.
func exhaustDestinationBudget(t *testing.T, s userManagementServer, mock *countingWhatsAppClient, accountPrefix string, phone string) {
	t.Helper()

	for i := 0; i < allowedPhoneDestinationSends; i++ {
		token := addDestinationTestUser(t, accountPrefix+string(rune('a'+i))+"@test.com", "")
		if _, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: phone}); err != nil {
			t.Fatalf("send %d of %d to the destination failed: %s", i+1, allowedPhoneDestinationSends, err.Error())
		}
	}
	if sent := mock.sendsTo(phone); sent != allowedPhoneDestinationSends {
		t.Fatalf("expected %d messages to the destination, the mock recorded %d", allowedPhoneDestinationSends, sent)
	}
}

// assertResourceExhausted checks the endpoint refused for budget reasons rather than any of the
// other reasons these endpoints can refuse for.
func assertResourceExhausted(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatalf("the send was accepted, the destination's window was already full")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected a gRPC status error, got %v", err)
	}
	if st.Code() != codes.ResourceExhausted {
		t.Errorf("expected %s, got %s: %s", codes.ResourceExhausted, st.Code(), st.Message())
	}
}

// TestAddPhoneNumberCapsSendsPerDestinationAcrossAccounts is the finding itself: the attacker
// rotates accounts, and the victim's number still receives only what its own window allows.
func TestAddPhoneNumberCapsSendsPerDestinationAcrossAccounts(t *testing.T) {
	const victim = "+391230030001"

	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)

	exhaustDestinationBudget(t, s, mockWhatsApp, "f10_add_", victim)

	// A brand new account, its own budget untouched, asking for one more message to the number.
	attacker := addDestinationTestUser(t, "f10_add_rotated@test.com", "")
	_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &attacker, NewPhone: victim})
	assertResourceExhausted(t, err)

	if sent := mockWhatsApp.sendsTo(victim); sent != allowedPhoneDestinationSends {
		t.Errorf(
			"the victim's number received %d messages, the window allows %d however many accounts ask",
			sent, allowedPhoneDestinationSends,
		)
	}

	// The per-account reservation runs first, by design: it is what makes the uniqueness probe
	// of F-08 cost something. A caller refused by the destination has therefore still spent one
	// of its own three slots, and that is what is asserted here rather than what one might
	// prefer. Reversing the order would hand back a free probe, which is the worse trade.
	user, err := testUserDBService.GetUserByID(attacker.InstanceId, attacker.Id)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if spent := len(user.Account.PhoneVerificationAttempts); spent != 1 {
		t.Errorf("refused caller has %d per-account slots spent, expected 1 (the account reservation precedes the destination check)", spent)
	}
}

// TestEditPhoneNumberCapsSendsPerDestinationAcrossAccounts covers the second endpoint that pays
// for a message: changing an existing number to the victim's.
func TestEditPhoneNumberCapsSendsPerDestinationAcrossAccounts(t *testing.T) {
	const (
		victim = "+391230030002"
		owned  = "+391230030102"
	)

	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)

	exhaustDestinationBudget(t, s, mockWhatsApp, "f10_edit_", victim)

	attacker := addDestinationTestUser(t, "f10_edit_rotated@test.com", owned)
	_, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &attacker, NewPhone: victim})
	assertResourceExhausted(t, err)

	if sent := mockWhatsApp.sendsTo(victim); sent != allowedPhoneDestinationSends {
		t.Errorf("EditPhoneNumber pushed the victim's number to %d messages, the window allows %d", sent, allowedPhoneDestinationSends)
	}
}

// TestResendContactVerificationCapsSendsPerDestinationAcrossAccounts covers the third endpoint:
// the resend on a number the account already holds unverified. Since F-09 several accounts can
// hold the same unverified number at once, so this is a live route to the victim.
func TestResendContactVerificationCapsSendsPerDestinationAcrossAccounts(t *testing.T) {
	const victim = "+391230030003"

	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)

	exhaustDestinationBudget(t, s, mockWhatsApp, "f10_resend_", victim)

	attacker := addDestinationTestUser(t, "f10_resend_rotated@test.com", victim)
	_, err := s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
		Token:   &attacker,
		Address: victim,
		Type:    models.ContactTypePhone,
	})
	assertResourceExhausted(t, err)

	if sent := mockWhatsApp.sendsTo(victim); sent != allowedPhoneDestinationSends {
		t.Errorf("ResendContactVerification pushed the victim's number to %d messages, the window allows %d", sent, allowedPhoneDestinationSends)
	}
}

// TestPhoneDestinationRecordsOutliveTheirWindow guards the one thing the TTL index and the rate
// limit window have to agree on: a record must not be swept away while it still holds sends that
// count, or the cap would silently reset early.
func TestPhoneDestinationRecordsOutliveTheirWindow(t *testing.T) {
	if userdb.PhoneVerificationSendRetention < phoneDestinationRateLimitWindow {
		t.Errorf(
			"send records are kept %d s but the rate limit window is %d s: a record can expire while its sends still count",
			userdb.PhoneVerificationSendRetention, phoneDestinationRateLimitWindow,
		)
	}
}

// TestPhoneProbeDoesNotSpendTheDestinationBudget covers the one thing a budget keyed by
// destination must not do: let a caller spend somebody else's number.
//
// The uniqueness check refuses a number another participant has verified, and that refusal sends
// nothing. If it were charged to the number, anyone could walk a victim's number down to zero and
// leave its owner unable to ask for their own code. The per-account budget still pays for the
// probe, which is what F-08 put there; the number's budget is only touched once a message is
// actually about to be sent.
func TestPhoneProbeDoesNotSpendTheDestinationBudget(t *testing.T) {
	const (
		addTarget  = "+391230030005"
		editTarget = "+391230030006"
		proberOwn  = "+391230030106"
	)

	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)

	// probeBudgetSpent reports how much of the number's window the refused probes consumed.
	probeBudgetSpent := func(t *testing.T, phone string) int {
		t.Helper()
		spent, err := testUserDBService.PhoneDestinationSendsInWindow(testInstanceID, phone, phoneDestinationRateLimitWindow)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		return spent
	}

	t.Run("AddPhoneNumber probes leave the number untouched", func(t *testing.T) {
		seedPhoneOwner(t, "f10_probe_add_owner@test.com", addTarget)
		prober := addDestinationTestUser(t, "f10_probe_add@test.com", "")

		// Three probes is the caller's own whole window, so this is as much probing as one
		// account can do at all.
		for i := 0; i < allowedPhoneVerificationAttempts; i++ {
			_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &prober, NewPhone: addTarget})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("probe %d: expected InvalidArgument, got %v", i+1, err)
			}
		}

		if spent := probeBudgetSpent(t, addTarget); spent != 0 {
			t.Errorf("refused probes spent %d of the number's sends; a probe delivers nothing and must cost the number nothing", spent)
		}
		if mockWhatsApp.sendsTo(addTarget) != 0 {
			t.Errorf("a refused probe reached Meta")
		}
	})

	t.Run("EditPhoneNumber probes leave the number untouched", func(t *testing.T) {
		seedPhoneOwner(t, "f10_probe_edit_owner@test.com", editTarget)
		prober := addDestinationTestUser(t, "f10_probe_edit@test.com", proberOwn)

		for i := 0; i < allowedPhoneVerificationAttempts; i++ {
			_, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &prober, NewPhone: editTarget})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("probe %d: expected InvalidArgument, got %v", i+1, err)
			}
		}

		if spent := probeBudgetSpent(t, editTarget); spent != 0 {
			t.Errorf("refused probes spent %d of the number's sends; a probe delivers nothing and must cost the number nothing", spent)
		}
		if mockWhatsApp.sendsTo(editTarget) != 0 {
			t.Errorf("a refused probe reached Meta")
		}
	})
}
