package whatsappqueue

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coneno/logger"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeMessaging answers DeleteOutgoingWhatsAppForUser from a script of errors, one per call (nil
// means success); calls past the script succeed. Every other method of the interface is left nil.
type fakeMessaging struct {
	messageAPI.MessagingServiceApiClient

	mu       sync.Mutex
	script   []error
	requests []*messageAPI.DeleteOutgoingWhatsAppForUserReq
	callAt   []time.Time
	// block, when set, makes each call wait for the context to end (a messaging that hangs).
	block bool
	// release, when set, makes each call wait until it is closed.
	release chan struct{}
}

func (f *fakeMessaging) DeleteOutgoingWhatsAppForUser(ctx context.Context, in *messageAPI.DeleteOutgoingWhatsAppForUserReq, _ ...grpc.CallOption) (*messageAPI.DeleteOutgoingWhatsAppForUserResp, error) {
	f.mu.Lock()
	n := len(f.requests)
	f.requests = append(f.requests, in)
	f.callAt = append(f.callAt, time.Now())
	block, release := f.block, f.release
	var err error
	if n < len(f.script) {
		err = f.script[n]
	}
	f.mu.Unlock()

	if release != nil {
		<-release
	}
	if block {
		<-ctx.Done()
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if err != nil {
		return nil, err
	}
	return &messageAPI.DeleteOutgoingWhatsAppForUserResp{RemovedFromQueue: 1}, nil
}

func (f *fakeMessaging) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// fastCanceller has the default number of attempts with short waits.
func fastCanceller(client messageAPI.MessagingServiceApiClient) *Canceller {
	c := NewCanceller(client)
	c.Backoff = []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	c.AttemptTimeout = time.Second
	return c
}

func captureErrors(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	original := logger.Error.Writer()
	logger.Error.SetOutput(&buf)
	defer logger.Error.SetOutput(original)
	fn()
	return buf.String()
}

func unavailable() error { return status.Error(codes.Unavailable, "connection refused to 10.0.0.1") }

func TestDefaultPolicyIsThreeAttempts(t *testing.T) {
	c := NewCanceller(&fakeMessaging{})
	if len(c.Backoff)+1 != 3 || c.Backoff[0] != time.Second || c.Backoff[1] != 2*time.Second {
		t.Errorf("unexpected retry policy: %v", c.Backoff)
	}
	if c.AttemptTimeout != 5*time.Second {
		t.Errorf("unexpected attempt timeout: %v", c.AttemptTimeout)
	}
}

func TestCancelPassesTheRequestThrough(t *testing.T) {
	f := &fakeMessaging{}
	if ok := fastCanceller(f).Cancel("italy", "u1", []string{"weekly"}, false); !ok {
		t.Fatal("expected success")
	}
	if f.calls() != 1 {
		t.Fatalf("expected one call, got %d", f.calls())
	}
	req := f.requests[0]
	if req.InstanceId != "italy" || req.UserId != "u1" || len(req.MessageTypes) != 1 || req.MessageTypes[0] != "weekly" || req.PurgeArchive {
		t.Errorf("unexpected request: %+v", req)
	}

	f = &fakeMessaging{}
	fastCanceller(f).Cancel("italy", "u2", nil, true)
	if req := f.requests[0]; len(req.MessageTypes) != 0 || !req.PurgeArchive {
		t.Errorf("unexpected purge request: %+v", req)
	}
}

func TestCancelRetriesUntilMessagingAnswers(t *testing.T) {
	f := &fakeMessaging{script: []error{unavailable(), status.Error(codes.DeadlineExceeded, "slow")}}
	c := fastCanceller(f)
	var ok bool
	logged := captureErrors(t, func() { ok = c.Cancel("italy", "u3", nil, false) })
	if !ok {
		t.Fatal("messaging answered on the third attempt: expected success")
	}
	if f.calls() != 3 {
		t.Errorf("expected 3 calls, got %d", f.calls())
	}
	if strings.Contains(logged, "WA_ALERT") {
		t.Errorf("an alert was logged although the cancellation succeeded: %q", logged)
	}
	// The waits between attempts are the backoff.
	if gap := f.callAt[1].Sub(f.callAt[0]); gap < c.Backoff[0] {
		t.Errorf("second attempt after %v, want at least %v", gap, c.Backoff[0])
	}
	if gap := f.callAt[2].Sub(f.callAt[1]); gap < c.Backoff[1] {
		t.Errorf("third attempt after %v, want at least %v", gap, c.Backoff[1])
	}
}

func TestCancelGivesUpAfterTheLastAttemptWithAnAlert(t *testing.T) {
	f := &fakeMessaging{script: []error{unavailable(), unavailable(), unavailable(), nil}}
	var ok bool
	logged := captureErrors(t, func() { ok = fastCanceller(f).Cancel("italy", "user-down", nil, false) })
	if ok {
		t.Error("expected failure")
	}
	if f.calls() != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", f.calls())
	}
	if !strings.Contains(logged, "WA_ALERT cancel_failed user=user-down attempts=3 code=Unavailable") {
		t.Errorf("missing or malformed alert: %q", logged)
	}
	// Only the user ID and the code: nothing messaging said.
	if strings.Contains(logged, "10.0.0.1") {
		t.Errorf("the alert carries the error text: %q", logged)
	}
}

func TestCancelDoesNotRetryWhatARetryCannotFix(t *testing.T) {
	for _, code := range []codes.Code{codes.InvalidArgument, codes.Unimplemented, codes.PermissionDenied, codes.FailedPrecondition} {
		t.Run(code.String(), func(t *testing.T) {
			f := &fakeMessaging{script: []error{status.Error(code, "no")}}
			var ok bool
			logged := captureErrors(t, func() { ok = fastCanceller(f).Cancel("italy", "u4", nil, false) })
			if ok || f.calls() != 1 {
				t.Errorf("expected one failed attempt, got ok=%v calls=%d", ok, f.calls())
			}
			if !strings.Contains(logged, "WA_ALERT cancel_failed user=u4 attempts=1") {
				t.Errorf("missing alert: %q", logged)
			}
		})
	}
}

func TestEachAttemptIsBoundedByTheTimeout(t *testing.T) {
	f := &fakeMessaging{block: true}
	c := fastCanceller(f)
	c.AttemptTimeout = 30 * time.Millisecond
	var ok bool
	var logged string
	done := make(chan struct{})
	go func() {
		defer close(done)
		logged = captureErrors(t, func() { ok = c.Cancel("italy", "u5", nil, false) })
	}()
	// 3 timeouts + 2 waits, with generous slack; without the timeout Cancel never returns.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Cancel did not return: an attempt is not bounded by the timeout")
	}
	if ok || f.calls() != 3 {
		t.Errorf("a hanging messaging must time out on every attempt: ok=%v calls=%d", ok, f.calls())
	}
	if !strings.Contains(logged, "code=DeadlineExceeded") {
		t.Errorf("unexpected alert: %q", logged)
	}
}

func TestCancelWithoutAMessagingClientAlerts(t *testing.T) {
	var nilCanceller *Canceller
	for name, c := range map[string]*Canceller{"nil canceller": nilCanceller, "nil client": NewCanceller(nil)} {
		t.Run(name, func(t *testing.T) {
			var ok bool
			logged := captureErrors(t, func() {
				ok = c.Cancel("italy", "u6", nil, false)
				c.CancelInBackground("italy", "u6", nil, false)
				c.Wait()
			})
			if ok {
				t.Error("expected failure")
			}
			if strings.Count(logged, "WA_ALERT cancel_failed user=u6") != 2 {
				t.Errorf("expected an alert per call: %q", logged)
			}
		})
	}
}

func TestCancelInBackgroundReturnsAtOnceAndWaitWaits(t *testing.T) {
	f := &fakeMessaging{release: make(chan struct{})}
	c := fastCanceller(f)

	returned := make(chan struct{})
	go func() {
		c.CancelInBackground("italy", "u7", nil, false)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("CancelInBackground waited for messaging")
	}

	waited := make(chan struct{})
	go func() {
		c.Wait()
		close(waited)
	}()
	select {
	case <-waited:
		t.Fatal("Wait returned before the cancellation finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(f.release)
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after the cancellation finished")
	}
	if f.calls() != 1 {
		t.Errorf("expected one call, got %d", f.calls())
	}
}
