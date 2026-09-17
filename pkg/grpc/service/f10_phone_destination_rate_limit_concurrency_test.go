package service

import (
	"context"
	"fmt"
	"sync"
	"testing"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAddPhoneNumberCapsSendsPerDestinationUnderConcurrency is the finding driven the way it would
// actually be driven. The accounts an attacker rotates through share nothing, so their requests do
// not queue behind one another: they arrive together, and the per-account budget that each of them
// passes trivially is no defence at all. What has to hold is that the destination's own budget is
// consumed atomically, so that the number of messages that leave for one number is the window's,
// whatever order the requests landed in.
//
// The number is used by no other test, so its record does not exist when the requests start and
// they race to create it. A refusal is a valid outcome here; an error is not, and neither is a
// fourth message.
func TestAddPhoneNumberCapsSendsPerDestinationUnderConcurrency(t *testing.T) {
	const (
		victim           = "+391230030004"
		parallelAccounts = 20
	)

	mockWhatsApp := &countingWhatsAppClient{}
	s := newRateLimitTestServer(mockWhatsApp)

	// Every account is created up front and is fresh, so none of them is ever refused for its own
	// budget and the only thing that can refuse them is the number's.
	tokens := make([]api_types.TokenInfos, parallelAccounts)
	for i := range tokens {
		tokens[i] = addDestinationTestUser(t, fmt.Sprintf("f10_conc_%02d@test.com", i), "")
	}

	errs := make([]error, parallelAccounts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			<-start
			_, errs[slot] = s.AddPhoneNumber(context.Background(), &api.PhoneMsg{
				Token:    &tokens[slot],
				NewPhone: victim,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	accepted := 0
	for i, err := range errs {
		switch code := status.Code(err); code {
		case codes.OK:
			accepted++
		case codes.ResourceExhausted:
			// The number's window was full, which is the whole point.
		default:
			t.Errorf("account %d was neither served nor refused for budget: %s (%v)", i, code, err)
		}
	}

	if accepted != allowedPhoneDestinationSends {
		t.Errorf(
			"%d of %d rotated accounts got a message through to one number, the window allows exactly %d",
			accepted, parallelAccounts, allowedPhoneDestinationSends,
		)
	}
	if sent := mockWhatsApp.sendsTo(victim); sent != allowedPhoneDestinationSends {
		t.Errorf(
			"the victim's number received %d messages from %d concurrent accounts, the window allows %d",
			sent, parallelAccounts, allowedPhoneDestinationSends,
		)
	}
}
