package userdb

import (
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// TestDbReservePhoneDestinationSendSlotUnderConcurrency is the property the per-destination
// budget exists for: the accounts an attacker rotates through are independent of each other, so
// their requests arrive together and the only thing standing between them and an unbounded number
// of paid messages to one victim is that the check and the increment are a single atomic
// operation.
//
// The number is used by no other test, so the budget document does not exist when the goroutines
// start. That is deliberate: it is the case where several requests race to create the same
// document and one of them loses on the duplicate key, which is exactly the path a careless
// implementation would surface as an error instead of as a refusal.
func TestDbReservePhoneDestinationSendSlotUnderConcurrency(t *testing.T) {
	const (
		phone            = "+391230020100"
		parallelRequests = 20
	)

	results := make([]bool, parallelRequests)
	errs := make([]error, parallelRequests)

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < parallelRequests; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			<-start
			results[slot], errs[slot] = testDBService.ReservePhoneDestinationSendSlot(
				testInstanceID, phone, testDestinationMaxSends, testDestinationWindow)
		}(i)
	}
	close(start)
	wg.Wait()

	reserved := 0
	for i, ok := range results {
		if errs[i] != nil {
			t.Errorf("request %d failed instead of being answered: %s", i, errs[i].Error())
			continue
		}
		if ok {
			reserved++
		}
	}

	if reserved != testDestinationMaxSends {
		t.Errorf(
			"%d of %d concurrent requests reserved a send slot for one number, the window allows exactly %d",
			reserved, parallelRequests, testDestinationMaxSends,
		)
	}

	doc := storedSendRecord(t, phone)
	sends, ok := doc["sends"].(bson.A)
	if !ok {
		t.Fatalf("send record holds no sends array: %v", doc)
	}
	if len(sends) != reserved {
		t.Errorf(
			"%d slots were granted but %d sends are recorded: the reservations were not atomic",
			reserved, len(sends),
		)
	}
}
