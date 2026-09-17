package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// The interleaving F-02 describes cannot be pinned to a single suspension point: the window
// that matters sits between the moment a request reads the account and the moment it stores the
// code, and both ends of it are database calls, with no seam the test can hold open (the send
// hook the rate-limit tests use fires after the store). So this test overlaps the three
// endpoints that issue a code many times over and checks an invariant that must hold whatever
// order the writes landed in: no code that was handed to one number may ever confirm another.
//
// TestDbStaleCodeCannotFollowAReplacedNumber commits the same interleaving one database
// operation at a time, deterministically.

// deliveryRecorder remembers which code went to which number.
type deliveryRecorder struct {
	mu         sync.Mutex
	deliveries []delivery
}

type delivery struct {
	phone string
	code  string
}

func (r *deliveryRecorder) SendVerificationCode(ctx context.Context, toPhoneNumber, code, lang string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliveries = append(r.deliveries, delivery{phone: toPhoneNumber, code: code})
	return nil
}

func (r *deliveryRecorder) SendTemplateMessage(ctx context.Context, toPhoneNumber, templateName, lang string, params map[string]string) error {
	return nil
}

// last returns the most recent delivery, for a reader running while the race is still on.
func (r *deliveryRecorder) last() (delivery, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.deliveries) == 0 {
		return delivery{}, false
	}
	return r.deliveries[len(r.deliveries)-1], true
}

func (r *deliveryRecorder) snapshot() []delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]delivery, len(r.deliveries))
	copy(out, r.deliveries)
	return out
}

func addBindingRaceUser(t *testing.T, accountID string, phone string) api_types.TokenInfos {
	t.Helper()
	users, err := addTestUsers([]models.User{{
		Account: models.Account{
			Type: "email", AccountID: accountID,
			AccountConfirmedAt:        time.Now().Unix(),
			PhoneVerificationAttempts: []int64{},
		},
		ContactInfos: []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone},
		},
	}})
	if err != nil {
		t.Fatalf("failed to create test user: %s", err.Error())
	}
	return api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}
}

// assertConfirmedOnlyTheDeliveredNumber is the positive half of the invariant, checked on the
// user the endpoint returned: a verification that succeeds must confirm exactly the number the
// code it accepted was delivered to, leave no other phone contact confirmed, and turn the
// whatsapp channel on for it. Codes are six random digits, so the code the mock handed to a
// number identifies that number.
func assertConfirmedOnlyTheDeliveredNumber(t *testing.T, round int, user *api.User, deliveredTo string) {
	t.Helper()
	if user == nil {
		t.Errorf("round %d: a successful verification returned no user", round)
		return
	}
	confirmed := []string{}
	for _, ci := range user.ContactInfos {
		if ci.GetType() == models.ContactTypePhone && ci.GetConfirmedAt() > 0 {
			confirmed = append(confirmed, ci.GetPhone())
		}
	}
	if len(confirmed) != 1 {
		t.Errorf("round %d: a successful verification left %d confirmed phone contacts %v, expected exactly one",
			round, len(confirmed), confirmed)
		return
	}
	if confirmed[0] != deliveredTo {
		t.Errorf("round %d: the verification confirmed %s with a code delivered to %s", round, confirmed[0], deliveredTo)
	}
	if !hasChannel(user.ContactPreferences.GetPreferredChannels(), models.ChannelWhatsApp) {
		t.Errorf("round %d: %s was verified but the whatsapp channel is off: %v",
			round, confirmed[0], user.ContactPreferences.GetPreferredChannels())
	}
}

func TestConcurrentChangePhoneAndResendCannotConfirmForeignNumber(t *testing.T) {
	const rounds = 40
	// How often a verification actually landed while the numbers were still moving.
	var confirmedDuringRace int64

	for round := 0; round < rounds; round++ {
		first := fmt.Sprintf("+3912400%05d", round*3)
		second := fmt.Sprintf("+3912400%05d", round*3+1)
		third := fmt.Sprintf("+3912400%05d", round*3+2)

		recorder := &deliveryRecorder{}
		s := newRateLimitTestServer(recorder)
		// The verifier below keeps trying codes while the numbers move under it, so most of its
		// attempts are stale. The cap is raised for the round so that spent attempts cannot
		// remove the phone before the assertions run; the attempt budget is M-7's subject, not
		// this test's.
		s.Intervals.MaxVerificationAttempts = 1000
		token := addBindingRaceUser(t, fmt.Sprintf("binding_race_%d@test.com", round), first)

		// Three requests for the same account, released together: a resend for the number it
		// carries and two changes to different numbers. Whichever wins, the account ends with
		// one number and one pending code.
		start := make(chan struct{})
		var racers sync.WaitGroup
		racersDone := make(chan struct{})
		// Errors are expected for the requests that lose the race; what matters here is only
		// which code each number actually received.
		requests := []func(){
			func() {
				_, _ = s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
					Token: &token, Type: models.ContactTypePhone, Address: first,
				})
			},
			func() {
				_, _ = s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: second})
			},
			func() {
				_, _ = s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: third})
			},
		}
		for _, request := range requests {
			racers.Add(1)
			go func(run func()) {
				defer racers.Done()
				<-start
				run()
			}(request)
		}

		// A fourth participant in the race: while the three requests are in flight it keeps
		// trying the code the mock delivered most recently. Every success is checked on the
		// spot, so the window in which a number is confirmed is covered, not only the state
		// left behind once the dust has settled.
		var verifier sync.WaitGroup
		verifier.Add(1)
		go func() {
			defer verifier.Done()
			<-start
			for {
				select {
				case <-racersDone:
					return
				default:
				}
				sent, ok := recorder.last()
				if !ok {
					time.Sleep(200 * time.Microsecond)
					continue
				}
				resp, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: sent.code})
				if err == nil {
					atomic.AddInt64(&confirmedDuringRace, 1)
					assertConfirmedOnlyTheDeliveredNumber(t, round, resp, sent.phone)
				}
			}
		}()

		close(start)
		racers.Wait()
		close(racersDone)
		verifier.Wait()

		user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
		if err != nil {
			t.Fatalf("round %d: unexpected error: %s", round, err.Error())
		}
		current, found := phoneOf(t, token.Id)
		if !found {
			t.Fatalf("round %d: the account lost its phone number", round)
		}

		// Every code that reached a number other than the one the account now carries must be
		// unable to confirm it, and must leave the account exactly as it found it. The
		// comparison is against the state just before each attempt rather than against an
		// unconfirmed account, because the verifier above may legitimately have confirmed the
		// number this round, which is the case the assertion must not mistake for the bug.
		for _, sent := range recorder.snapshot() {
			if sent.phone == current.Phone {
				continue
			}
			before, hadPhone := phoneOf(t, token.Id)
			if !hadPhone {
				t.Fatalf("round %d: the account lost its phone number", round)
			}
			channelsBefore := channelsOf(t, token.Id)

			_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: sent.code})
			if err == nil {
				t.Fatalf("round %d: the code sent to %s confirmed %s (stored code was bound to %q)",
					round, sent.phone, current.Phone, user.Account.PhoneVerificationCode.Phone)
			}
			after, stillThere := phoneOf(t, token.Id)
			if !stillThere {
				t.Fatalf("round %d: a foreign code cost the participant the registered number", round)
			}
			if after.Phone != before.Phone || after.ConfirmedAt != before.ConfirmedAt {
				t.Fatalf("round %d: a code sent to %s changed the registered number: %+v became %+v",
					round, sent.phone, before, after)
			}
			if hasChannel(channelsOf(t, token.Id), models.ChannelWhatsApp) && !hasChannel(channelsBefore, models.ChannelWhatsApp) {
				t.Fatalf("round %d: a code sent to %s enabled whatsapp for %s", round, sent.phone, after.Phone)
			}
		}
	}

	// The in-race assertion is only worth having if verifications do land while the numbers are
	// moving; they land dozens of times per run. Requiring one keeps the positive half of the
	// invariant from silently becoming dead code.
	if atomic.LoadInt64(&confirmedDuringRace) == 0 {
		t.Error("no verification succeeded during the race, so the in-race assertion never ran")
	}
}
