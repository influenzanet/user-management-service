package service

import (
	"context"
	"fmt"
	"sync"
	"testing"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// F-09 removed the rule that made a number exclusive before anybody had proved anything, so for
// the first time two accounts can hold the same number, both unverified, both with a live code,
// and verify it at the same moment. That is a deliberate residual, not an oversight: settling it
// needs uniqueness kept outside the contact array (a collection keyed by the number, written with
// the confirmation) or a transaction, which is a schema decision; a partial unique index on the
// array would not do, since partial filters apply per document, not per element. A residual that
// is only asserted in a commit message is worth nothing, so this test states where its edges are.
//
// There is no seam to hold open: both ends of the window are database calls. So, like the F-02
// concurrency test, this overlaps the operation many times over and asserts the invariants that
// must hold whatever order the writes landed in, rather than a particular interleaving.
//
// Enforced on every round, and this is what bounds the damage:
//   - a contact that is verified is never removed from any account, by any release;
//   - nobody is ever confirmed for a number they did not hold;
//   - somebody always wins, which is structural rather than lucky: VerifyWhatsAppCode calls
//     FinalizePhoneVerification and only then, in the same handler and strictly after it,
//     ReleaseUnverifiedPhoneClaims. An account can only lose its claim to a release, and a
//     release only runs once the account that triggered it has already been confirmed. "Both
//     fail" is therefore unreachable. If those two calls are ever reordered, this assumption
//     goes with them and this test should fail loudly rather than turn flaky.
//
// Checked only when a round happens to serialise, which with the barrier is rare: the loser-side
// assertions below, that a released claimant keeps no usable code and can verify nothing
// afterwards. Do not read this test's green as proof of that path. It is pinned deterministically,
// on every run and without goroutines, by TestVerifyWhatsAppCodeReleasesUnverifiedClaims and by
// TestConcurrentAddPhoneNumberOnOneNumber. Making both accounts land inside the window is what
// makes the residual below reproduce every time, and that is this test's real job.
//
// Does not hold, and this is the residual: BOTH accounts can end up confirmed on the number.
// The test accepts either outcome on purpose. Asserting "exactly one wins" would write today's
// bug into the suite as a requirement; asserting "both win" would do the same the day an index
// makes it one. The day that changes, the invariants below still have to pass.

const concurrentClaimRounds = 6

// concurrentVerifyOutcome is what one account's verification round produced. The token is held
// by pointer: api_types.TokenInfos is a protobuf message and carries a mutex, so copying one is
// a vet error.
type concurrentVerifyOutcome struct {
	token *api_types.TokenInfos
	err   error
}

func TestVerifyWhatsAppCodeConcurrentClaimsOnOneNumber(t *testing.T) {
	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3

	for round := 0; round < concurrentClaimRounds; round++ {
		// A number of its own per round, so one round cannot decide the next.
		phone := fmt.Sprintf("+39123000130%d", round)
		codeA := "500001"
		codeB := "500002"

		first := addVerifyCodeTestUser(t, fmt.Sprintf("f09_race_a_%d@test.com", round), phone, validCode(codeA), nil, nil)
		second := addVerifyCodeTestUser(t, fmt.Sprintf("f09_race_b_%d@test.com", round), phone, validCode(codeB), nil, nil)

		// Both hold the number unverified before the race: that is the state F-09 allows and
		// the earlier rule forbade.
		for _, token := range []*api_types.TokenInfos{&first, &second} {
			if ci, found := phoneOf(t, token.Id); !found || ci.Phone != phone || ci.ConfirmedAt != 0 {
				t.Fatalf("round %d: the race does not start from two unverified claims: %+v", round, ci)
			}
		}

		outcomes := make([]concurrentVerifyOutcome, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, pair := range []struct {
			token *api_types.TokenInfos
			code  string
		}{{&first, codeA}, {&second, codeB}} {
			wg.Add(1)
			go func(i int, token *api_types.TokenInfos, code string) {
				defer wg.Done()
				<-start
				_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: token, Code: code})
				outcomes[i] = concurrentVerifyOutcome{token: token, err: err}
			}(i, pair.token, pair.code)
		}
		close(start)
		wg.Wait()

		confirmed := 0
		for _, outcome := range outcomes {
			user, err := testUserDBService.GetUserByID(testInstanceID, outcome.token.Id)
			if err != nil {
				t.Fatalf("round %d: unexpected error: %s", round, err.Error())
			}
			ci, holdsPhone := user.FindContactInfoByTypeAndAddr(models.ContactTypePhone, phone)

			if outcome.err == nil {
				// A verification that returned success must have left the number confirmed on
				// that account: no release may take a proven contact away again.
				if !holdsPhone {
					t.Errorf("round %d: a verified contact was removed from the account that proved it", round)
					continue
				}
				if ci.ConfirmedAt <= 0 {
					t.Errorf("round %d: verification succeeded but the contact is not confirmed: %+v", round, ci)
					continue
				}
				confirmed++
				if !hasChannel(channelsOf(t, outcome.token.Id), models.ChannelWhatsApp) {
					t.Errorf("round %d: a confirmed number did not enable the whatsapp channel", round)
				}
				continue
			}

			// The loser. Its claim is released, so it must keep neither the contact nor a
			// usable code, and it must not be confirmed for a number it never proved.
			if holdsPhone && ci.ConfirmedAt > 0 {
				t.Errorf("round %d: a failed verification left the number confirmed: %+v", round, ci)
			}
			// Read after both goroutines have joined on purpose: the release is two writes, the
			// $pull of the contact and then the $set clearing the code, so mid-flight a loser can
			// legitimately hold no contact and a code that has not been cleared yet. Not
			// exploitable — a verify in that window fails at finalize, the contact being gone —
			// but it is only reliably empty once the winner's handler has returned.
			if user.Account.PhoneVerificationCode.Code != "" && !holdsPhone {
				t.Errorf("round %d: a released claim kept a usable code: %+v", round, user.Account.PhoneVerificationCode)
			}
		}

		if confirmed == 0 {
			t.Errorf("round %d: two live codes for one number and nobody could verify it", round)
		}
		// Reported rather than asserted: which of the two outcomes a round produces depends on
		// how the writes interleaved. Seeing "2" in this output is the residual reproducing,
		// and seeing only "1" across every round means the rounds serialised, not that the
		// residual is gone. Whoever adds the unique index should expect this to read 1
		// everywhere afterwards, with every assertion above still passing.
		t.Logf("round %d: %d of 2 accounts ended verified on %s", round, confirmed, phone)

		// Whoever lost cannot come back with the code they were holding.
		for _, outcome := range outcomes {
			if outcome.err == nil {
				continue
			}
			code := codeA
			if outcome.token.Id == second.Id {
				code = codeB
			}
			_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: outcome.token, Code: code})
			// The exact answer, not merely an error: finalize names the phone in an $elemMatch,
			// so once the contact is pulled it returns mongo.ErrNoDocuments, which the handler
			// maps to this message.
			if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "no phone verification in progress" {
				t.Errorf("round %d: a released claimant could still use its code: %v", round, err)
			}
		}
	}
}

// TestReleaseNeverStripsAVerifiedContactUnderLoad drives the release itself concurrently against
// one account that has already proved the number. Whatever else happens, a proven contact stays.
func TestReleaseNeverStripsAVerifiedContactUnderLoad(t *testing.T) {
	const phone = "+391230001320"
	const parallelReleases = 8

	owner := addVerifyCodeTestUser(t, "f09_release_load_owner@test.com", phone, models.VerificationCode{}, nil, nil)
	if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, owner.Id, phone); err != nil {
		t.Fatalf("failed to seed the verified owner: %s", err.Error())
	}
	claimant := addVerifyCodeTestUser(t, "f09_release_load_claimant@test.com", phone, validCode("600001"), nil, nil)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < parallelReleases; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Every caller names the owner as the account that proved the number, which is what
			// VerifyWhatsAppCode does; overlapping them must stay a no-op after the first.
			if _, err := testUserDBService.ReleaseUnverifiedPhoneClaims(testInstanceID, phone, owner.Id); err != nil {
				t.Errorf("unexpected error: %s", err.Error())
			}
		}()
	}
	close(start)
	wg.Wait()

	ci, found := phoneOf(t, owner.Id)
	if !found || ci.ConfirmedAt <= 0 {
		t.Errorf("concurrent releases removed a verified contact: %+v", ci)
	}
	if _, stillThere := phoneOf(t, claimant.Id); stillThere {
		t.Error("the unverified claim survived the releases")
	}
}

// TestConcurrentAddPhoneNumberOnOneNumber drives the contention through the endpoint rather than
// from seeded documents: two accounts add the same number at the same moment. Both succeed and
// both end up holding it unverified — the accepted residual, asserted here explicitly so that it
// is on the record as known behaviour rather than discovered later in production. The first to
// verify then takes it, and the other is released.
func TestConcurrentAddPhoneNumberOnOneNumber(t *testing.T) {
	const phone = "+391230001330"
	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3

	first := addRateLimitTestUser(t, "f09_addrace_a@test.com", nil)
	second := addRateLimitTestUser(t, "f09_addrace_b@test.com", nil)

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, token := range []*api_types.TokenInfos{&first, &second} {
		wg.Add(1)
		go func(i int, token *api_types.TokenInfos) {
			defer wg.Done()
			<-start
			_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: token, NewPhone: phone})
			errs[i] = err
		}(i, token)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("account %d could not add a number nobody had proved: %v", i, err)
		}
	}
	// The residual, stated as such: an unverified claim is not exclusive any more, so both hold it.
	for i, token := range []*api_types.TokenInfos{&first, &second} {
		ci, found := phoneOf(t, token.Id)
		if !found || ci.Phone != phone || ci.ConfirmedAt != 0 {
			t.Fatalf("account %d does not hold the number unverified: %+v", i, ci)
		}
	}

	winnerCode := storedPhoneCode(t, first.Id)
	loserCode := storedPhoneCode(t, second.Id)
	if winnerCode == "" || loserCode == "" {
		t.Fatal("both accounts should be holding a live code")
	}

	if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &first, Code: winnerCode}); err != nil {
		t.Fatalf("the first account could not verify: %s", err.Error())
	}

	if ci, found := phoneOf(t, first.Id); !found || ci.ConfirmedAt <= 0 {
		t.Errorf("the winner's number was not confirmed: %+v", ci)
	}
	if _, stillThere := phoneOf(t, second.Id); stillThere {
		t.Error("the loser's claim survived")
	}
	loser, err := testUserDBService.GetUserByID(testInstanceID, second.Id)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if loser.Account.PhoneVerificationCode.Code != "" {
		t.Errorf("the loser kept a usable code: %+v", loser.Account.PhoneVerificationCode)
	}
	_, err = s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &second, Code: loserCode})
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "no phone verification in progress" {
		t.Errorf("the loser's code was still usable: %v", err)
	}
}
