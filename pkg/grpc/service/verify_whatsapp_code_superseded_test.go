package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A verification request reads the account's code, then charges an attempt to it. Requests
// that read the code before another request verified, replaced or deleted it are superseded:
// they must charge nothing, remove nothing and, when they carry the right code for a number the
// account now holds verified, answer like the verification they repeat.

// holdVerifications makes the first n requests that reach VerifyWhatsAppCode's hook wait there
// until release is called. held returns once all n are waiting.
func holdVerifications(t *testing.T, n int32) (held func(), release func()) {
	t.Helper()
	var arrived int32
	var waiting sync.WaitGroup
	waiting.Add(int(n))
	gate := make(chan struct{})
	afterVerificationCodeReadHook = func(string) {
		if atomic.AddInt32(&arrived, 1) <= n {
			waiting.Done()
			<-gate
		}
	}
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() {
		release()
		afterVerificationCodeReadHook = nil
	})
	return waiting.Wait, release
}

type verifyResult struct {
	user *api.User
	err  error
}

// verifyHeldInBackground starts n requests with code; they are expected to stop at the hook.
func verifyHeldInBackground(s *userManagementServer, token *api_types.TokenInfos, code string, n int) (wait func() []verifyResult) {
	results := make([]verifyResult, n)
	var done sync.WaitGroup
	for i := 0; i < n; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			user, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: token, Code: code})
			results[i] = verifyResult{user: user, err: err}
		}(i)
	}
	return func() []verifyResult {
		done.Wait()
		return results
	}
}

// The reproduction of the review: four requests with the right code read it, a fifth verifies,
// then the four go on. With the cap at 3 they used to charge 1, 2, 3 against the cleared code,
// and the fourth removed the number that had just been verified.
// Fails if the increment is not bound to the code the request read, or if the cap removal is
// not conditional on it.
func TestVerifyWhatsAppCodeSupersededRequestsKeepTheVerifiedPhone(t *testing.T) {
	for _, tt := range []struct {
		name      string
		staleCode string
		wantCode  codes.Code
	}{
		{"with the right code they repeat the verification", "246810", codes.OK},
		{"with a wrong code they verify nothing", "000000", codes.InvalidArgument},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newRateLimitTestServer(&countingWhatsAppClient{})
			s.Intervals.MaxVerificationAttempts = 3
			queue := &queueRecorder{}
			withQueue(&s, queue)
			phone := auditPhone()
			token := addVerifyCodeTestUser(t, "superseded_"+tt.staleCode+"@test.com", phone, validCode("246810"), nil, nil)

			const stale = 4
			held, release := holdVerifications(t, stale)
			wait := verifyHeldInBackground(&s, &token, tt.staleCode, stale)
			held()

			if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "246810"}); err != nil {
				t.Fatalf("the verification that was not held failed: %v", err)
			}
			release()

			for i, r := range wait() {
				if got := status.Code(r.err); got != tt.wantCode {
					t.Errorf("superseded request %d answered %v, want %v", i, r.err, tt.wantCode)
				}
				if tt.wantCode == codes.OK && r.user == nil {
					t.Errorf("superseded request %d succeeded without the account", i)
				}
			}
			ci, found := phoneOf(t, token.Id)
			if !found || ci.Phone != phone || ci.ConfirmedAt <= 0 {
				t.Fatalf("the verified phone did not survive the superseded requests: found=%v %+v", found, ci)
			}
			if !hasChannel(channelsOf(t, token.Id), models.ChannelWhatsApp) {
				t.Errorf("the whatsapp channel was removed: %v", channelsOf(t, token.Id))
			}
			user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
			if user.Account.PhoneVerificationCode.Attempts != 0 || user.Account.PhoneVerificationCode.CodeHash != "" {
				t.Errorf("the superseded requests charged the cleared code: %+v", user.Account.PhoneVerificationCode)
			}
			s.whatsAppQueue.Wait()
			if calls := queue.calls(); len(calls) != 0 {
				t.Errorf("queued messages were cancelled: %v", calls)
			}
		})
	}
}

// A request that read code A while a resend replaced it with B charges nothing to B, whatever
// it submits, and never removes the number, even when B is at its cap: those attempts are not
// the request's. The participant is told there is nothing to verify and types the new code.
// Fails if the increment or the cap removal follows whichever code the account holds now.
func TestVerifyWhatsAppCodeAcrossAResendChargesNothing(t *testing.T) {
	for i, tt := range []struct {
		name       string
		bAttempts  int64
		submitted  string
		wantStatus codes.Code
	}{
		{"with code A", 0, "111111", codes.InvalidArgument},
		{"with code B", 0, "222222", codes.InvalidArgument},
		{"with B at its cap", 3, "111111", codes.InvalidArgument},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newRateLimitTestServer(&countingWhatsAppClient{})
			s.Intervals.MaxVerificationAttempts = 3
			phone := auditPhone()
			token := addVerifyCodeTestUser(t, fmt.Sprintf("resend_%d@test.com", i), phone, validCode("111111"), nil, nil)

			held, release := holdVerifications(t, 1)
			wait := verifyHeldInBackground(&s, &token, tt.submitted, 1)
			held()
			codeB := validCode("222222")
			codeB.Phone = phone
			codeB.Attempts = tt.bAttempts
			codeB.CreatedAt++ // a resend comes later
			if err := testUserDBService.SetPhoneVerificationCode(testInstanceID, token.Id, codeB.stored(t, token.Id)); err != nil {
				t.Fatalf("replace the code: %v", err)
			}
			release()

			if r := wait()[0]; status.Code(r.err) != tt.wantStatus {
				t.Fatalf("the request across the resend answered %v, want %v", r.err, tt.wantStatus)
			}
			user, _ := testUserDBService.GetUserByID(testInstanceID, token.Id)
			if user.Account.PhoneVerificationCode.Attempts != tt.bAttempts {
				t.Errorf("code B was charged: attempts %d, want %d", user.Account.PhoneVerificationCode.Attempts, tt.bAttempts)
			}
			if ci, found := phoneOf(t, token.Id); !found || ci.Phone != phone || ci.ConfirmedAt != 0 {
				t.Errorf("the pending number changed: found=%v %+v", found, ci)
			}
		})
	}
}

// A repeated verification is audited as such, not as a second verification.
func TestAuditVerifyWhatsAppCodeRepeatedVerification(t *testing.T) {
	s, rec := auditTestServer(t, &countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3
	phone := auditPhone()
	token := addVerifyCodeTestUser(t, "audit_verify_repeat@test.com", phone, validCode("135790"), nil, nil)

	held, release := holdVerifications(t, 1)
	wait := verifyHeldInBackground(s, &token, "135790", 1)
	held()
	if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "135790"}); err != nil {
		t.Fatal(err)
	}
	requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_VERIFICATION, logEv, "outcome=verified phone="+masked(phone), phone)
	release()
	if r := wait()[0]; r.err != nil {
		t.Fatalf("the repeated verification failed: %v", r.err)
	}
	requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_VERIFICATION, logEv, "outcome=already_verified phone="+masked(phone), phone)
}
