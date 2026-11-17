package userdb

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

var rateLimitCounterFields = []string{"failedLoginAttempts", "passwordResetTriggers", "phoneVerificationAttempts"}

func rawAccount(t *testing.T, userID string) bson.Raw {
	t.Helper()
	id, err := primitive.ObjectIDFromHex(userID)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := testDBService.collectionRefUsers(testInstanceID).FindOne(context.Background(), bson.M{"_id": id}).DecodeBytes()
	if err != nil {
		t.Fatal(err)
	}
	return doc.Lookup("account").Document()
}

// storedCounter reads a counter as stored, failing unless it is an array.
func storedCounter(t *testing.T, userID, field string) []int64 {
	t.Helper()
	value := rawAccount(t, userID).Lookup(field)
	if value.Type != bsontype.Array {
		t.Fatalf("account.%s is stored as %s, not as an array", field, value.Type)
	}
	values, err := value.Array().Values()
	if err != nil {
		t.Fatal(err)
	}
	out := []int64{}
	for _, v := range values {
		out = append(out, v.AsInt64())
	}
	return out
}

func addCounterTestUser(t *testing.T, account models.Account) string {
	t.Helper()
	account.AccountID = primitive.NewObjectID().Hex() + "@counters.test"
	id, err := testDBService.AddUser(testInstanceID, models.User{Account: account})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setAccountField(t *testing.T, userID, field string, value interface{}) {
	t.Helper()
	id, _ := primitive.ObjectIDFromHex(userID)
	update := bson.M{"$set": bson.M{"account." + field: value}} // nil is stored as null
	if _, err := testDBService.collectionRefUsers(testInstanceID).UpdateOne(context.Background(), bson.M{"_id": id}, update); err != nil {
		t.Fatal(err)
	}
}

func unsetAccountField(t *testing.T, userID, field string) {
	t.Helper()
	id, _ := primitive.ObjectIDFromHex(userID)
	if _, err := testDBService.collectionRefUsers(testInstanceID).UpdateOne(context.Background(), bson.M{"_id": id},
		bson.M{"$unset": bson.M{"account." + field: ""}}); err != nil {
		t.Fatal(err)
	}
}

// Whatever path creates an account (the admin endpoint and the admin tool leave the counters
// nil), it is stored with empty arrays, never null.
func TestAddUserStoresEmptyRateLimitCounters(t *testing.T) {
	id := addCounterTestUser(t, models.Account{})
	for _, field := range rateLimitCounterFields {
		if got := storedCounter(t, id, field); len(got) != 0 {
			t.Errorf("account.%s: expected an empty array, got %v", field, got)
		}
	}

	// Counters given by the caller are kept as they are.
	given := models.Account{
		FailedLoginAttempts:       []int64{11},
		PasswordResetTriggers:     []int64{22},
		PhoneVerificationAttempts: []int64{33},
	}
	id = addCounterTestUser(t, given)
	for field, want := range map[string][]int64{
		"failedLoginAttempts":       given.FailedLoginAttempts,
		"passwordResetTriggers":     given.PasswordResetTriggers,
		"phoneVerificationAttempts": given.PhoneVerificationAttempts,
	} {
		if got := storedCounter(t, id, field); !reflect.DeepEqual(got, want) {
			t.Errorf("account.%s: expected %v, got %v", field, want, got)
		}
	}
}

type counterWriter struct {
	field     string
	retention int64
	record    func(instanceID, userID string) error
}

func counterWriters() []counterWriter {
	return []counterWriter{
		{"failedLoginAttempts", FailedLoginAttemptRetention, testDBService.SaveFailedLoginAttempt},
		{"passwordResetTriggers", PasswordResetTriggerRetention, testDBService.SavePasswordResetTrigger},
	}
}

// A failed login or a reset request is recorded even on an account whose counter is null or
// missing (created before the counters were initialised): the limiter must not fail open there.
func TestRateLimitCounterRecordsOnNullOrMissingArray(t *testing.T) {
	for _, w := range counterWriters() {
		for _, state := range []string{"null", "missing"} {
			t.Run(w.field+"/"+state, func(t *testing.T) {
				id := addCounterTestUser(t, models.Account{})
				if state == "null" {
					setAccountField(t, id, w.field, nil)
				} else {
					unsetAccountField(t, id, w.field)
				}
				before := time.Now().Unix()
				if err := w.record(testInstanceID, id); err != nil {
					t.Fatalf("the attempt was not recorded: %v", err)
				}
				got := storedCounter(t, id, w.field)
				if len(got) != 1 || got[0] < before || got[0] > time.Now().Unix() {
					t.Errorf("expected one entry for now, got %v", got)
				}
			})
		}
	}
}

// Recording an attempt drops the entries older than the counter's retention and keeps the
// others; the other counters of the account, and other accounts, are untouched.
func TestRateLimitCounterPrunesOnRecord(t *testing.T) {
	for _, w := range counterWriters() {
		t.Run(w.field, func(t *testing.T) {
			now := time.Now().Unix()
			expired := now - w.retention - 5
			kept := now - w.retention + 30
			recent := now - 10
			others := []int64{expired, recent}
			account := models.Account{
				FailedLoginAttempts:       others,
				PasswordResetTriggers:     others,
				PhoneVerificationAttempts: others,
			}
			id := addCounterTestUser(t, account)
			bystander := addCounterTestUser(t, account)
			setAccountField(t, id, w.field, []int64{expired, kept, recent})

			if err := w.record(testInstanceID, id); err != nil {
				t.Fatal(err)
			}
			got := storedCounter(t, id, w.field)
			if len(got) != 3 || got[0] != kept || got[1] != recent || got[2] < now {
				t.Errorf("expected [%d %d <now>], got %v", kept, recent, got)
			}
			for _, field := range rateLimitCounterFields {
				if field != w.field {
					if got := storedCounter(t, id, field); !reflect.DeepEqual(got, others) {
						t.Errorf("account.%s changed: %v", field, got)
					}
				}
				if got := storedCounter(t, bystander, field); !reflect.DeepEqual(got, others) {
					t.Errorf("another account's %s changed: %v", field, got)
				}
			}
		})
	}
}

func TestRateLimitCounterRefusesInvalidUserID(t *testing.T) {
	for _, w := range counterWriters() {
		if err := w.record(testInstanceID, "not-an-object-id"); err == nil {
			t.Errorf("%s: an invalid user ID was accepted", w.field)
		}
	}
}

// Concurrent failed logins are all counted: the append is one update on the stored array, not a
// read followed by a write.
func TestRateLimitCounterKeepsConcurrentRecords(t *testing.T) {
	for _, w := range counterWriters() {
		t.Run(w.field, func(t *testing.T) {
			id := addCounterTestUser(t, models.Account{})
			const n = 25
			var wg sync.WaitGroup
			errs := make(chan error, n)
			for i := 0; i < n; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- w.record(testInstanceID, id)
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := storedCounter(t, id, w.field); len(got) != n {
				t.Errorf("expected %d entries, got %d", n, len(got))
			}
		})
	}
}

// The counters are written only by their own appends. A full-user save carrying a copy read
// earlier would erase the attempts recorded since, so UpdateUser refuses them.
func TestUpdateUserRefusesRateLimitCounters(t *testing.T) {
	for _, field := range []string{"account.failedLoginAttempts", "account.passwordResetTriggers"} {
		t.Run(field, func(t *testing.T) {
			id := addCounterTestUser(t, models.Account{
				FailedLoginAttempts:   []int64{time.Now().Unix()},
				PasswordResetTriggers: []int64{time.Now().Unix()},
			})
			stale, err := testDBService.GetUserByID(testInstanceID, id)
			if err != nil {
				t.Fatal(err)
			}
			stale.Account.FailedLoginAttempts = []int64{}
			stale.Account.PasswordResetTriggers = []int64{}
			if _, err := testDBService.UpdateUser(testInstanceID, stale, field); err == nil {
				t.Error("UpdateUser accepted a rate-limit counter")
			}
			for _, f := range []string{"failedLoginAttempts", "passwordResetTriggers"} {
				if got := storedCounter(t, id, f); len(got) != 1 {
					t.Errorf("account.%s was overwritten: %v", f, got)
				}
			}
		})
	}
}
