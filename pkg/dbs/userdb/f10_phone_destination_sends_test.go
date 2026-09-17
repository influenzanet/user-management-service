package userdb

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

// F-10: the phone verification budget used to be counted per account only, so rotating accounts
// bought an attacker as many verification messages to one victim number as they cared to create
// accounts for. These tests pin the second budget, the one counted per destination number.

const (
	testDestinationMaxSends = 10
	testDestinationWindow   = int64(3600)
)

// storedSendRecord reads the raw document a destination's budget is kept in, so a test can assert
// on what is actually on disk rather than on what the reservation returned.
func storedSendRecord(t *testing.T, phone string) bson.M {
	t.Helper()

	ctx, cancel := testDBService.getContext()
	defer cancel()

	var doc bson.M
	err := testDBService.collectionRefPhoneVerificationSends(testInstanceID).FindOne(
		ctx, bson.M{"_id": phoneVerificationSendKey(phone)},
	).Decode(&doc)
	if err != nil {
		t.Fatalf("unexpected error reading the send record: %s", err.Error())
	}
	return doc
}

// seedSendRecord writes a destination's budget directly, to place timestamps the test controls.
func seedSendRecord(t *testing.T, phone string, sends []int64) {
	t.Helper()

	ctx, cancel := testDBService.getContext()
	defer cancel()

	_, err := testDBService.collectionRefPhoneVerificationSends(testInstanceID).InsertOne(ctx, bson.M{
		"_id":       phoneVerificationSendKey(phone),
		"sends":     sends,
		"updatedAt": time.Now(),
	})
	if err != nil {
		t.Fatalf("unexpected error seeding the send record: %s", err.Error())
	}
}

func TestDbReservePhoneDestinationSendSlot(t *testing.T) {
	t.Run("the first sends of the window are allowed and the next one is not", func(t *testing.T) {
		const phone = "+391230020001"

		for i := 0; i < testDestinationMaxSends; i++ {
			reserved, err := testDBService.ReservePhoneDestinationSendSlot(
				testInstanceID, phone, testDestinationMaxSends, testDestinationWindow)
			if err != nil {
				t.Fatalf("unexpected error: %s", err.Error())
			}
			if !reserved {
				t.Fatalf("send %d of %d was refused, the window still had room for it", i+1, testDestinationMaxSends)
			}
		}

		reserved, err := testDBService.ReservePhoneDestinationSendSlot(
			testInstanceID, phone, testDestinationMaxSends, testDestinationWindow)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if reserved {
			t.Errorf("send %d was allowed, the window only allows %d", testDestinationMaxSends+1, testDestinationMaxSends)
		}
	})

	t.Run("a send older than the window frees a slot and is pruned", func(t *testing.T) {
		const phone = "+391230020002"

		// One expired send plus a window that is one short of full: without pruning the record
		// holds the whole cap and the reservation below would be refused.
		now := time.Now().Unix()
		seeded := []int64{now - testDestinationWindow - 60} // outside the window, must not count
		for i := 0; i < testDestinationMaxSends-1; i++ {
			seeded = append(seeded, now-int64(i)-1)
		}
		seedSendRecord(t, phone, seeded)

		reserved, err := testDBService.ReservePhoneDestinationSendSlot(
			testInstanceID, phone, testDestinationMaxSends, testDestinationWindow)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if !reserved {
			t.Fatalf("the expired send did not free a slot")
		}

		doc := storedSendRecord(t, phone)
		sends, ok := doc["sends"].(bson.A)
		if !ok {
			t.Fatalf("send record holds no sends array: %v", doc)
		}
		if len(sends) != testDestinationMaxSends {
			t.Errorf("expected the expired send to be pruned, leaving %d entries, got %d", testDestinationMaxSends, len(sends))
		}
		for _, entry := range sends {
			ts, ok := entry.(int64)
			if !ok {
				t.Fatalf("send timestamp is not an int64: %v", entry)
			}
			if ts <= now-testDestinationWindow {
				t.Errorf("an expired send survived the reservation: %d", ts)
			}
		}
	})

	t.Run("the destination number is never stored in clear", func(t *testing.T) {
		const phone = "+391230020003"

		if _, err := testDBService.ReservePhoneDestinationSendSlot(
			testInstanceID, phone, testDestinationMaxSends, testDestinationWindow); err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}

		doc := storedSendRecord(t, phone)
		id, ok := doc["_id"].(string)
		if !ok {
			t.Fatalf("send record has no string _id: %v", doc)
		}
		if id == phone {
			t.Errorf("the destination number is stored as the document key in clear")
		}
		if strings.Contains(id, strings.TrimPrefix(phone, "+")) {
			t.Errorf("the document key still contains the destination number: %s", id)
		}
		if len(id) != 64 {
			t.Errorf("expected a hex SHA-256 key of 64 characters, got %d: %s", len(id), id)
		}
		for field, value := range doc {
			text, isText := value.(string)
			if isText && strings.Contains(text, strings.TrimPrefix(phone, "+")) {
				t.Errorf("field %s leaks the destination number: %s", field, text)
			}
		}
	})

	t.Run("two spellings of one number share a budget", func(t *testing.T) {
		const (
			plain   = "+391230020004"
			spelled = "+39 123-002 (0004)"
		)

		for i := 0; i < testDestinationMaxSends; i++ {
			reserved, err := testDBService.ReservePhoneDestinationSendSlot(
				testInstanceID, plain, testDestinationMaxSends, testDestinationWindow)
			if err != nil {
				t.Fatalf("unexpected error: %s", err.Error())
			}
			if !reserved {
				t.Fatalf("send %d was refused, the window still had room for it", i+1)
			}
		}

		reserved, err := testDBService.ReservePhoneDestinationSendSlot(
			testInstanceID, spelled, testDestinationMaxSends, testDestinationWindow)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if reserved {
			t.Errorf("writing the same number with separators bought an extra send")
		}
	})

	t.Run("a zero padded international prefix shares the budget of the plain one", func(t *testing.T) {
		const (
			plain  = "+391230020007"
			padded = "+00391230020007"
		)

		for i := 0; i < testDestinationMaxSends; i++ {
			reserved, err := testDBService.ReservePhoneDestinationSendSlot(
				testInstanceID, plain, testDestinationMaxSends, testDestinationWindow)
			if err != nil {
				t.Fatalf("unexpected error: %s", err.Error())
			}
			if !reserved {
				t.Fatalf("send %d was refused, the window still had room for it", i+1)
			}
		}

		reserved, err := testDBService.ReservePhoneDestinationSendSlot(
			testInstanceID, padded, testDestinationMaxSends, testDestinationWindow)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if reserved {
			t.Errorf("padding the international prefix with zeros bought an extra send to the same number")
		}
	})

	t.Run("distinct numbers have distinct budgets", func(t *testing.T) {
		const (
			exhausted = "+391230020005"
			other     = "+391230020006"
		)

		for i := 0; i < testDestinationMaxSends; i++ {
			if _, err := testDBService.ReservePhoneDestinationSendSlot(
				testInstanceID, exhausted, testDestinationMaxSends, testDestinationWindow); err != nil {
				t.Fatalf("unexpected error: %s", err.Error())
			}
		}

		reserved, err := testDBService.ReservePhoneDestinationSendSlot(
			testInstanceID, other, testDestinationMaxSends, testDestinationWindow)
		if err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		if !reserved {
			t.Errorf("exhausting one number's budget also blocked a different number")
		}
	})
}

// TestDbPhoneVerificationSendsTTLIndex asserts the records expire on their own. The TTL monitor
// runs about once a minute, so the deletion itself is not observable in a unit test; what is
// observable, and what would actually be missing if the index were forgotten, is the index spec.
func TestDbPhoneVerificationSendsTTLIndex(t *testing.T) {
	if err := testDBService.CreateIndexForPhoneVerificationSends(testInstanceID); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}

	ctx, cancel := testDBService.getContext()
	defer cancel()

	cur, err := testDBService.collectionRefPhoneVerificationSends(testInstanceID).Indexes().List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	var specs []bson.M
	if err := cur.All(ctx, &specs); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}

	for _, spec := range specs {
		key, _ := spec["key"].(bson.M)
		if _, onUpdatedAt := key["updatedAt"]; !onUpdatedAt {
			continue
		}
		expire, found := spec["expireAfterSeconds"]
		if !found {
			t.Fatalf("the index on updatedAt is not a TTL index: %v", spec)
		}
		if got, want := expire, int32(PhoneVerificationSendRetention); got != want {
			t.Errorf("TTL is %v, expected %v seconds so a record outlives the rate limit window and no longer", got, want)
		}
		return
	}
	t.Errorf("no TTL index on updatedAt: the send records would grow forever, indexes found: %v", specs)
}
