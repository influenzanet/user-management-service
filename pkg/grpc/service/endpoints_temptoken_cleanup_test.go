package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/models"
)

// Both temp-token endpoints start the cleanup of expired tokens when it was not started for
// deleteTempTokensMinInterval. They run concurrently, so the decision is one atomic gate.

// resetTempTokenCleanup makes the next call due, and puts the gate back after the test.
func resetTempTokenCleanup(t *testing.T) {
	t.Helper()
	saved := lastTempTokenCleanup.Load()
	lastTempTokenCleanup.Store(0)
	t.Cleanup(func() { lastTempTokenCleanup.Store(saved) })
}

// Of many calls at the same moment exactly one starts the cleanup; the next one is due only once
// the interval has passed.
// Fails if the gate checks and sets in two steps, letting every call of a burst start a cleanup,
// or if the interval boundary moves.
func TestTempTokenCleanupDueOncePerInterval(t *testing.T) {
	resetTempTokenCleanup(t)
	now := time.Now().Unix()

	const callers = 100
	var due atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if tempTokenCleanupDue(now) {
				due.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := due.Load(); got != 1 {
		t.Fatalf("%d of %d concurrent calls started the cleanup, want exactly 1", got, callers)
	}

	if tempTokenCleanupDue(now + deleteTempTokensMinInterval) {
		t.Fatal("the cleanup was due again before the interval had passed")
	}
	if !tempTokenCleanupDue(now + deleteTempTokensMinInterval + 1) {
		t.Fatal("the cleanup was not due once the interval had passed")
	}
}

// The reproduction of the review: concurrent requests to both endpoints, with the cleanup due.
// The race detector, which CI runs, is the assertion; the requests must also all succeed.
func TestConcurrentTempTokenRequestsAreRaceFree(t *testing.T) {
	resetTempTokenCleanup(t)
	s := userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals:       models.Intervals{TokenExpiryInterval: time.Second * 2, VerificationCodeLifetime: 60},
	}

	const requests = 40
	errs := make([]error, requests)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			info := &api_types.TempTokenInfo{
				UserId:     fmt.Sprintf("cleanup_race_user_%d", i),
				InstanceId: testInstanceID,
				Purpose:    "cleanup_race",
			}
			<-start
			if i%2 == 0 {
				_, errs[i] = s.GetOrCreateTemptoken(context.Background(), info)
			} else {
				_, errs[i] = s.GenerateTempToken(context.Background(), info)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("request %d failed: %v", i, err)
		}
	}
}
