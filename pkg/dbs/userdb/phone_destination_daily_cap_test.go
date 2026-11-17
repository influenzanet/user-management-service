package userdb

import (
	"errors"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// A number may receive at most 10 verification codes an hour and 20 a day, across all accounts.
// Both limits are checked by one reservation on one array of timestamps.

const (
	testHourlyMax  = 10
	testHourWindow = int64(3600)
	testDailyMax   = 20
	testDayWindow  = int64(86400)
)

var testHourlyAndDailyLimits = []SendLimit{
	{MaxSends: testHourlyMax, WindowSeconds: testHourWindow},
	{MaxSends: testDailyMax, WindowSeconds: testDayWindow},
}

// reserveDestination reserves one slot and fails the test on an error.
func reserveDestination(t *testing.T, phone string, limits []SendLimit) bool {
	t.Helper()
	granted, err := testDBService.ReservePhoneDestinationSendSlot(testInstanceID, phone, limits)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	return granted
}

// storedSends returns the timestamps on disk for a number.
func storedSends(t *testing.T, phone string) []int64 {
	t.Helper()
	sends, ok := storedSendRecord(t, phone)["sends"].(bson.A)
	if !ok {
		t.Fatalf("send record holds no sends array")
	}
	out := make([]int64, 0, len(sends))
	for _, entry := range sends {
		ts, ok := entry.(int64)
		if !ok {
			t.Fatalf("send timestamp is not an int64: %v", entry)
		}
		out = append(out, ts)
	}
	return out
}

// spreadOverTheDay returns n timestamps between 2 and 23 hours ago: inside the day, outside the
// hour, so they count only towards the daily limit.
func spreadOverTheDay(n int) []int64 {
	now := time.Now().Unix()
	sends := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		sends = append(sends, now-2*3600-int64(i)*(21*3600/int64(n)))
	}
	return sends
}

func TestDbReservePhoneDestinationSendSlotHourlyAndDaily(t *testing.T) {
	t.Run("the 11th send within an hour is refused", func(t *testing.T) {
		const phone = "+391230021001"
		for i := 0; i < testHourlyMax; i++ {
			if !reserveDestination(t, phone, testHourlyAndDailyLimits) {
				t.Fatalf("send %d of %d in the hour was refused", i+1, testHourlyMax)
			}
		}
		if reserveDestination(t, phone, testHourlyAndDailyLimits) {
			t.Errorf("send %d within one hour was allowed, the hourly limit is %d", testHourlyMax+1, testHourlyMax)
		}
	})

	t.Run("the 21st send within a day is refused when spread over several hours", func(t *testing.T) {
		const phone = "+391230021002"
		// 15 sends earlier in the day, none in the last hour: 5 more fit the day, and the hour
		// is far from its limit when the 21st is asked for.
		seedSendRecord(t, phone, spreadOverTheDay(15))
		for i := 0; i < testDailyMax-15; i++ {
			if !reserveDestination(t, phone, testHourlyAndDailyLimits) {
				t.Fatalf("send %d of the day was refused, the day had room for it", 15+i+1)
			}
		}
		if reserveDestination(t, phone, testHourlyAndDailyLimits) {
			t.Errorf("send %d within one day was allowed, the daily limit is %d", testDailyMax+1, testDailyMax)
		}
		if got := len(storedSends(t, phone)); got != testDailyMax {
			t.Errorf("the refusal changed the record: %d sends stored, want %d", got, testDailyMax)
		}
	})

	t.Run("the limits apply whatever order they are given in", func(t *testing.T) {
		const phone = "+391230021003"
		reversed := []SendLimit{testHourlyAndDailyLimits[1], testHourlyAndDailyLimits[0]}
		seedSendRecord(t, phone, spreadOverTheDay(testDailyMax))
		if reserveDestination(t, phone, reversed) {
			t.Errorf("a number with a full day was granted a send when the daily limit came first")
		}

		const other = "+391230021004"
		for i := 0; i < testHourlyMax; i++ {
			if !reserveDestination(t, other, reversed) {
				t.Fatalf("send %d of %d in the hour was refused", i+1, testHourlyMax)
			}
		}
		if reserveDestination(t, other, reversed) {
			t.Errorf("a number with a full hour was granted a send when the daily limit came first")
		}
	})

	t.Run("sends older than a day stop counting and are pruned, younger ones are kept", func(t *testing.T) {
		const phone = "+391230021005"
		now := time.Now().Unix()
		expired := []int64{now - testDayWindow - 60, now - testDayWindow - 3600}
		seedSendRecord(t, phone, append(append([]int64{}, expired...), spreadOverTheDay(testDailyMax-1)...))

		if !reserveDestination(t, phone, testHourlyAndDailyLimits) {
			t.Fatalf("sends older than a day were counted towards the daily limit")
		}
		sends := storedSends(t, phone)
		if len(sends) != testDailyMax {
			t.Errorf("expected the %d sends of the day to be kept and the 2 expired ones dropped, %d stored", testDailyMax, len(sends))
		}
		for _, ts := range sends {
			if ts <= now-testDayWindow {
				t.Errorf("a send older than a day survived the reservation: %d", ts)
			}
		}
		// Pruning to the hour would lose what the day still counts and reopen the daily limit.
		olderThanAnHour := 0
		for _, ts := range sends {
			if ts <= now-testHourWindow {
				olderThanAnHour++
			}
		}
		if olderThanAnHour != testDailyMax-1 {
			t.Errorf("%d sends between one hour and one day old are kept, want %d", olderThanAnHour, testDailyMax-1)
		}
		if reserveDestination(t, phone, testHourlyAndDailyLimits) {
			t.Errorf("the 21st send of the day was allowed after a reservation pruned the record")
		}
	})

	t.Run("a refused send is not recorded", func(t *testing.T) {
		const phone = "+391230021006"
		seedSendRecord(t, phone, spreadOverTheDay(testDailyMax))
		for i := 0; i < 3; i++ {
			reserveDestination(t, phone, testHourlyAndDailyLimits)
		}
		if got := len(storedSends(t, phone)); got != testDailyMax {
			t.Errorf("refused attempts were recorded as sends: %d stored, want %d", got, testDailyMax)
		}
	})
}

// TestDbReservePhoneDestinationSendSlotDailyUnderConcurrency: requests arriving together never
// exceed either limit.
func TestDbReservePhoneDestinationSendSlotDailyUnderConcurrency(t *testing.T) {
	cases := []struct {
		name    string
		phone   string
		seeded  int
		granted int
	}{
		// 15 earlier in the day: the day has 5 left, the hour 10, so the day decides.
		{name: "the daily limit decides", phone: "+391230021101", seeded: 15, granted: testDailyMax - 15},
		// Nothing earlier: the hour decides.
		{name: "the hourly limit decides", phone: "+391230021102", seeded: 0, granted: testHourlyMax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.seeded > 0 {
				seedSendRecord(t, c.phone, spreadOverTheDay(c.seeded))
			}
			const parallelRequests = 25
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
						testInstanceID, c.phone, testHourlyAndDailyLimits)
				}(i)
			}
			close(start)
			wg.Wait()

			granted := 0
			for i, ok := range results {
				if errs[i] != nil {
					t.Errorf("request %d failed instead of being answered: %s", i, errs[i].Error())
				}
				if ok {
					granted++
				}
			}
			if granted != c.granted {
				t.Errorf("%d of %d concurrent requests were granted, want exactly %d", granted, parallelRequests, c.granted)
			}
			if got := len(storedSends(t, c.phone)); got != c.seeded+granted {
				t.Errorf("%d sends recorded for %d seeded and %d granted: the reservations were not atomic", got, c.seeded, granted)
			}
		})
	}
}

// TestDbReservePhoneDestinationSendSlotRefusesUnusableLimits: a budget that cannot be counted
// over its whole window must refuse the send, not grant it.
func TestDbReservePhoneDestinationSendSlotRefusesUnusableLimits(t *testing.T) {
	cases := []struct {
		name   string
		phone  string
		limits []SendLimit
	}{
		{name: "no limit at all", phone: "+391230021201", limits: nil},
		{name: "a zero window", phone: "+391230021202", limits: []SendLimit{{MaxSends: 10, WindowSeconds: 0}}},
		{name: "a negative window", phone: "+391230021203", limits: []SendLimit{{MaxSends: 10, WindowSeconds: -60}}},
		{
			name:   "a window longer than the records are kept",
			phone:  "+391230021204",
			limits: []SendLimit{{MaxSends: 10, WindowSeconds: 3600}, {MaxSends: 40, WindowSeconds: PhoneVerificationSendRetention + 1}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			granted, err := testDBService.ReservePhoneDestinationSendSlot(testInstanceID, c.phone, c.limits)
			if !errors.Is(err, ErrInvalidSendLimits) {
				t.Errorf("expected ErrInvalidSendLimits, got %v", err)
			}
			if granted {
				t.Errorf("the send was granted on unusable limits")
			}
			ctx, cancel := testDBService.getContext()
			defer cancel()
			count, err := testDBService.collectionRefPhoneVerificationSends(testInstanceID).CountDocuments(
				ctx, bson.M{"_id": phoneVerificationSendKey(c.phone)})
			if err != nil {
				t.Fatalf("unexpected error: %s", err.Error())
			}
			if count != 0 {
				t.Errorf("a record was written for a refused reservation")
			}
		})
	}

	t.Run("a window exactly as long as the records are kept is accepted", func(t *testing.T) {
		if !reserveDestination(t, "+391230021205", []SendLimit{{MaxSends: 1, WindowSeconds: PhoneVerificationSendRetention}}) {
			t.Errorf("the first send was refused")
		}
	})
}

// updatedAtTTLIndexes lists the indexes on updatedAt of an instance's send records.
func updatedAtTTLIndexes(t *testing.T, instanceID string) []bson.M {
	t.Helper()
	ctx, cancel := testDBService.getContext()
	defer cancel()
	cur, err := testDBService.collectionRefPhoneVerificationSends(instanceID).Indexes().List(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	var specs []bson.M
	if err := cur.All(ctx, &specs); err != nil {
		t.Fatalf("unexpected error: %s", err.Error())
	}
	var found []bson.M
	for _, spec := range specs {
		key, _ := spec["key"].(bson.M)
		if _, ok := key["updatedAt"]; ok {
			found = append(found, spec)
		}
	}
	return found
}

// isolatedInstance gives a test an instance database of its own, dropped before and after.
func isolatedInstance(t *testing.T, instanceID string) {
	t.Helper()
	drop := func() {
		ctx, cancel := testDBService.getContext()
		defer cancel()
		if err := testDBService.DBClient.Database(testDBService.DBNamePrefix + instanceID + "_users").Drop(ctx); err != nil {
			t.Fatalf("could not drop the test instance database: %s", err.Error())
		}
	}
	drop()
	t.Cleanup(drop)
}

func assertSingleDailyTTL(t *testing.T, instanceID string) {
	t.Helper()
	specs := updatedAtTTLIndexes(t, instanceID)
	if len(specs) != 1 {
		t.Fatalf("want exactly one index on updatedAt, found %d: %v", len(specs), specs)
	}
	if got := specs[0]["expireAfterSeconds"]; got != int32(86400) && got != int64(86400) {
		t.Errorf("expireAfterSeconds is %v (%T), want 86400", got, got)
	}
}

func TestDbCreateIndexForPhoneVerificationSendsTTL(t *testing.T) {
	t.Run("a fresh database gets the 24 h TTL", func(t *testing.T) {
		const instance = "ttl-fresh"
		isolatedInstance(t, instance)
		if err := testDBService.CreateIndexForPhoneVerificationSends(instance); err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		assertSingleDailyTTL(t, instance)
	})

	t.Run("an existing 1 h TTL is raised to 24 h", func(t *testing.T) {
		const instance = "ttl-upgrade"
		isolatedInstance(t, instance)
		ctx, cancel := testDBService.getContext()
		defer cancel()
		// The index an earlier version created.
		if _, err := testDBService.collectionRefPhoneVerificationSends(instance).Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "updatedAt", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(3600),
		}); err != nil {
			t.Fatalf("could not create the old index: %s", err.Error())
		}

		if err := testDBService.CreateIndexForPhoneVerificationSends(instance); err != nil {
			t.Fatalf("the old index was not updated: %s", err.Error())
		}
		assertSingleDailyTTL(t, instance)
	})

	t.Run("running it again changes nothing and does not fail", func(t *testing.T) {
		const instance = "ttl-rerun"
		isolatedInstance(t, instance)
		for i := 0; i < 2; i++ {
			if err := testDBService.CreateIndexForPhoneVerificationSends(instance); err != nil {
				t.Fatalf("run %d: unexpected error: %s", i+1, err.Error())
			}
		}
		assertSingleDailyTTL(t, instance)
	})

	t.Run("an index that is not on updatedAt alone is left alone", func(t *testing.T) {
		const instance = "ttl-compound"
		isolatedInstance(t, instance)
		ctx, cancel := testDBService.getContext()
		defer cancel()
		if _, err := testDBService.collectionRefPhoneVerificationSends(instance).Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{{Key: "updatedAt", Value: 1}, {Key: "sends", Value: 1}},
		}); err != nil {
			t.Fatalf("could not create the compound index: %s", err.Error())
		}
		if err := testDBService.CreateIndexForPhoneVerificationSends(instance); err != nil {
			t.Fatalf("unexpected error: %s", err.Error())
		}
		specs := updatedAtTTLIndexes(t, instance)
		ttl := 0
		for _, spec := range specs {
			if _, ok := spec["expireAfterSeconds"]; ok {
				ttl++
				if got := spec["expireAfterSeconds"]; got != int32(86400) && got != int64(86400) {
					t.Errorf("expireAfterSeconds is %v, want 86400", got)
				}
			}
		}
		if ttl != 1 || len(specs) != 2 {
			t.Errorf("want the compound index untouched and one 24 h TTL index next to it, found %v", specs)
		}
	})
}
