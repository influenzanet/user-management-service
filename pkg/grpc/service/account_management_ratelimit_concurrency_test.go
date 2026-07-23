package service

import (
	"context"
	"sync"
	"testing"
	"time"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Tests in this file are  meant to test the roboustness of the WhatsApp rate
// limiter implementation

// delay simulates the latency of the real HTTPS call to Meta.
//
// beforeSend, when set, runs inside the send and outside the mutex. It lets a test suspend a
// request precisely where production suspends it, waiting on Meta, so that other requests can be
// driven forward in between. This turns an otherwise schedule dependent race into a
// deterministic sequence while still calling the real endpoint.
type countingWhatsAppClient struct {
	mu         sync.Mutex
	sends      int
	recipients map[string]int
	delay      time.Duration
	err        error
	beforeSend func(toPhoneNumber string)
}

func (m *countingWhatsAppClient) SendVerificationCode(ctx context.Context, toPhoneNumber, code, lang string) error {
	m.mu.Lock()
	delay := m.delay
	err := m.err
	beforeSend := m.beforeSend
	m.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if beforeSend != nil {
		beforeSend(toPhoneNumber)
	}
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.sends++
	if m.recipients == nil {
		m.recipients = map[string]int{}
	}
	m.recipients[toPhoneNumber]++
	return nil
}

func (m *countingWhatsAppClient) SendTemplateMessage(ctx context.Context, toPhoneNumber, templateName, lang string, params map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

func (m *countingWhatsAppClient) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sends
}

// distinctRecipients returns how many different phone numbers received a code.
func (m *countingWhatsAppClient) distinctRecipients() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.recipients)
}

// sendBarrier holds every request that reaches SendVerificationCode until all of them
// have arrived or grace has elapsed since the first arrival, then releases them together.
//
// Without it the tests depend on the Go scheduler to interleave the requests and the result
// varies from run to run. This is meant to make the test reproducible.
//
// Note on the grace timeout: this is meant to keep the test valid when a proper fix is implemented
type sendBarrier struct {
	mu       sync.Mutex
	arrived  int
	want     int
	grace    time.Duration
	timerSet bool
	open     chan struct{}
	openOnce sync.Once
}

func newSendBarrier(want int, grace time.Duration) *sendBarrier {
	return &sendBarrier{want: want, grace: grace, open: make(chan struct{})}
}

func (b *sendBarrier) wait(string) {
	b.mu.Lock()
	b.arrived++
	everyoneHere := b.arrived >= b.want
	if !b.timerSet {
		b.timerSet = true
		time.AfterFunc(b.grace, b.release)
	}
	b.mu.Unlock()

	if everyoneHere {
		b.release()
	}
	<-b.open
}

func (b *sendBarrier) release() {
	b.openOnce.Do(func() { close(b.open) })
}

func newRateLimitTestServer(client WhatsAppClient) userManagementServer {
	return userManagementServer{
		userDBservice:   testUserDBService,
		globalDBService: testGlobalDBService,
		Intervals: models.Intervals{
			TokenExpiryInterval:      time.Second * 2,
			VerificationCodeLifetime: 60,
		},
		whatsAppClient: client,
		whatsAppConfig: config.WhatsAppConfig{
			Enabled:                  true,
			VerificationTemplateLang: "en",
		},
	}
}

// addRateLimitTestUser creates a user with a confirmed email, no phone number and the given
// verification attempts already on record, and returns a token for it.
func addRateLimitTestUser(t *testing.T, accountID string, alreadyUsed []int64) api_types.TokenInfos {
	t.Helper()

	users, err := addTestUsers([]models.User{
		{
			Account: models.Account{
				Type:                      "email",
				AccountID:                 accountID,
				PhoneVerificationAttempts: alreadyUsed,
			},
			ContactInfos: []models.ContactInfo{
				{
					ID:          primitive.NewObjectID(),
					Type:        "email",
					Email:       accountID,
					ConfirmedAt: time.Now().Unix(),
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

// recentAttempts returns timestamps inside the current rate limit window, to pre-load a user
// with attempts that have already been consumed.
func recentAttempts(n int) []int64 {
	now := time.Now().Unix()
	attempts := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		attempts = append(attempts, now-int64(i)-1)
	}
	return attempts
}

// TestAddPhoneNumberRateLimitUnderConcurrency asserts the two properties the rate limiter added
// for M-7 is supposed to provide, when AddPhoneNumber is called concurrently:
//
//  1. no more than allowedPhoneVerificationAttempts messages leave per window;
//  2. every message that left is still counted afterwards, so the limit also holds for the
//     sequential requests that follow.
func TestAddPhoneNumberRateLimitUnderConcurrency(t *testing.T) {
	const parallelRequests = 10

	mockWhatsApp := &countingWhatsAppClient{}
	barrier := newSendBarrier(parallelRequests, 2*time.Second)
	mockWhatsApp.beforeSend = barrier.wait

	s := newRateLimitTestServer(mockWhatsApp)
	token := addRateLimitTestUser(t, "test_for_add_phone_concurrency@test.com", nil)

	// Release all requests at the same instant so they overlap on the read.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < parallelRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := &api.PhoneMsg{
				Token:    &token,
				NewPhone: "+391234567900",
			}
			// Errors are expected for the requests that lose the race, only the
			// number of messages actually handed to Meta matters here.
			_, _ = s.AddPhoneNumber(context.Background(), req)
		}()
	}
	close(start)
	wg.Wait()

	sends := mockWhatsApp.count()
	if sends > allowedPhoneVerificationAttempts {
		t.Errorf(
			"rate limit bypassed: %d verification messages were sent from %d concurrent requests, but only %d are allowed per %d s window",
			sends, parallelRequests, allowedPhoneVerificationAttempts, phoneVerificationRateLimitWindow,
		)
	}

	user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if recorded := len(user.Account.PhoneVerificationAttempts); recorded < sends {
		t.Errorf(
			"attempt counter lost writes: %d messages were sent but only %d attempts are recorded on the user",
			sends, recorded,
		)
	}
}

// TestAddPhoneNumberHonoursRemainingBudgetWhenRequestsOverlap asserts the same two properties as
// above, but deterministically and against a user who has almost exhausted the window, which is
// the case that matters in practice
//
// The user starts with allowedPhoneVerificationAttempts-1 attempts already recorded inside the
// window, so exactly one send remains. Two requests then overlap:
//
//	request A: enters AddPhoneNumber, blocks inside SendVerificationCode (as it would on Meta)
//	request B: runs AddPhoneNumber to completion, sends, records its attempt
//	request A: released, finishes, its UpdateUser replaces the whole document with the copy
//	           it read before B existed, discarding B's recorded attempt
func TestAddPhoneNumberHonoursRemainingBudgetWhenRequestsOverlap(t *testing.T) {
	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)

	alreadyUsed := recentAttempts(allowedPhoneVerificationAttempts - 1)
	remainingBudget := allowedPhoneVerificationAttempts - len(alreadyUsed)

	token := addRateLimitTestUser(t, "test_for_overlapping_budget@test.com", alreadyUsed)

	// Both requests nominate the same number, so B takes the "existing unverified phone,
	// allow re-sending the code" path rather than being rejected for having another phone.
	const phone = "+391234567812"

	firstEnteredSend := make(chan struct{})
	releaseFirstSend := make(chan struct{})
	suspendToken := make(chan struct{}, 1)
	suspendToken <- struct{}{}
	mockWhatsApp.beforeSend = func(string) {
		select {
		case <-suspendToken:
			close(firstEnteredSend)
			<-releaseFirstSend
		default:
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: phone})
	}()

	// A is now stopped inside its send simulating where production waits on Meta.
	<-firstEnteredSend

	// B runs to completion while A is suspended.
	_, _ = s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: phone})

	close(releaseFirstSend)
	wg.Wait()

	sends := mockWhatsApp.count()
	if sends > remainingBudget {
		t.Errorf(
			"budget exceeded: %d messages sent while only %d of the %d per-window sends remained",
			sends, remainingBudget, allowedPhoneVerificationAttempts,
		)
	}

	user, err := testUserDBService.GetUserByID(testInstanceID, token.Id)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	if recorded, want := len(user.Account.PhoneVerificationAttempts), len(alreadyUsed)+sends; recorded != want {
		t.Errorf(
			"attempt accounting is wrong: %d attempts recorded, expected %d (%d pre-existing + %d sent)",
			recorded, want, len(alreadyUsed), sends,
		)
	}
}

// TestAddPhoneNumberBurstReachesDistinctNumbers asserts that one authenticated session cannot
// deliver verification codes to more distinct phone numbers than the window allows
// because messages are sent on the platform's Meta account and billed to it.
func TestAddPhoneNumberBurstReachesDistinctNumbers(t *testing.T) {
	// Each request nominates a different destination, as an attacker would.
	targets := []string{
		"+391234567801", "+391234567802", "+391234567803", "+391234567804", "+391234567805",
		"+391234567806", "+391234567807", "+391234567808", "+391234567809", "+391234567810",
	}

	mockWhatsApp := &countingWhatsAppClient{}
	barrier := newSendBarrier(len(targets), 2*time.Second)
	mockWhatsApp.beforeSend = barrier.wait

	s := newRateLimitTestServer(mockWhatsApp)
	token := addRateLimitTestUser(t, "test_for_add_phone_distinct_numbers@test.com", nil)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, target := range targets {
		wg.Add(1)
		go func(phone string) {
			defer wg.Done()
			<-start
			_, _ = s.AddPhoneNumber(context.Background(), &api.PhoneMsg{
				Token:    &token,
				NewPhone: phone,
			})
		}(target)
	}
	close(start)
	wg.Wait()

	if reached := mockWhatsApp.distinctRecipients(); reached > allowedPhoneVerificationAttempts {
		t.Errorf(
			"one session delivered codes to %d distinct phone numbers from %d concurrent requests; the limit is %d sends per %d s window",
			reached, len(targets), allowedPhoneVerificationAttempts, phoneVerificationRateLimitWindow,
		)
	}
}

// TestAddPhoneNumberRateLimitWithRealisticSendLatency asserts the limit holds for traffic that is
// not simultaneous at all, which is what arrives from the internet.
//
// The other tests force the requests to overlap, so it might be tempting to object that real
// clients cannot be that precise, but in producton is actually worse since the unguarded window runs from
// GetUserByID to SavePhoneVerificationAttempt and it contains the HTTPS round trip to Meta. In
// the other tests the mock returns instantly, so the window is only a few DB operations wide (microseconds),
// while in production it is as wide as the Meta call, typically 100-500 milliseconds
//
// Here the mock sleeps to model that latency and the requests are fired deliberately staggered,
// one every 50 ms. Every request that starts before the first one finishes its send still reads
// pre-send state and still passes the check, so the exploit needs no simultaneity
func TestAddPhoneNumberRateLimitWithRealisticSendLatency(t *testing.T) {
	const (
		metaLatency     = 400 * time.Millisecond
		requestSpacing  = 50 * time.Millisecond
		requestsToFire  = 6
		phoneUnderBurst = "+391234567811"
	)

	mockWhatsApp := &countingWhatsAppClient{delay: metaLatency}
	s := newRateLimitTestServer(mockWhatsApp)
	token := addRateLimitTestUser(t, "test_for_add_phone_send_latency@test.com", nil)

	// Last request starts at 250 ms, comfortably inside the 400 ms send of the first one.
	var wg sync.WaitGroup
	for i := 0; i < requestsToFire; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.AddPhoneNumber(context.Background(), &api.PhoneMsg{
				Token:    &token,
				NewPhone: phoneUnderBurst,
			})
		}()
		time.Sleep(requestSpacing)
	}
	wg.Wait()

	if sends := mockWhatsApp.count(); sends > allowedPhoneVerificationAttempts {
		t.Errorf(
			"rate limit bypassed without any simultaneity: %d messages sent from %d requests fired %v apart (send latency %v), limit is %d per %d s",
			sends, requestsToFire, requestSpacing, metaLatency, allowedPhoneVerificationAttempts, phoneVerificationRateLimitWindow,
		)
	}
}