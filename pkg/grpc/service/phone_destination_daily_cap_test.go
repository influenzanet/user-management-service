package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// Every endpoint that sends a verification code reserves against the daily cap, not only the
// hourly one: once a number has had 20 codes in a day, no endpoint sends it a 21st, even when
// the last hour had few.

// sendFromFreshAccounts adds the number to `count` fresh accounts, one send each, and fails the
// test if any of them is refused.
func sendFromFreshAccounts(t *testing.T, s userManagementServer, prefix string, phone string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		token := addDestinationTestUser(t, fmt.Sprintf("%s%02d@test.com", prefix, i), "")
		if _, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: phone}); err != nil {
			t.Fatalf("send %d of %d to the number failed: %s", i+1, count, err.Error())
		}
	}
}

// sendRecords is the collection of per-number send records of the test instance.
func sendRecords() *mongo.Collection {
	return testUserDBService.GetCollection(testInstanceID, userdb.PhoneVerificationSendCollection)
}

// clearSendRecords empties the per-number send records, so the number under test is the only one
// the test writes. Every other test uses numbers of its own and only reads their records within
// the test, so nothing they rely on is lost.
func clearSendRecords(t *testing.T) {
	t.Helper()
	ctx, cancel := testUserDBService.GetContext()
	defer cancel()
	if _, err := sendRecords().DeleteMany(ctx, bson.M{}); err != nil {
		t.Fatalf("could not clear the send records: %s", err.Error())
	}
}

// moveSendsOutOfTheHour shifts every recorded send two hours back: they still count for the day,
// no longer for the hour. It fails the test unless exactly one record (the number under test,
// after clearSendRecords) is moved.
func moveSendsOutOfTheHour(t *testing.T) {
	t.Helper()
	ctx, cancel := testUserDBService.GetContext()
	defer cancel()
	res, err := sendRecords().UpdateMany(ctx,
		bson.M{},
		bson.A{bson.M{"$set": bson.M{"sends": bson.M{"$map": bson.M{
			"input": "$sends",
			"in":    bson.M{"$subtract": bson.A{"$$this", int64(2 * 3600)}},
		}}}}},
	)
	if err != nil {
		t.Fatalf("could not move the sends back: %s", err.Error())
	}
	if res.MatchedCount != 1 {
		t.Fatalf("expected to move the sends of one number, matched %d records", res.MatchedCount)
	}
}

func TestEndpointsCapSendsPerDestinationPerDay(t *testing.T) {
	cases := []struct {
		name   string
		victim string
		send   func(s userManagementServer, prefix string, victim string) error
	}{
		{
			name:   "AddPhoneNumber",
			victim: "+393230031001",
			send: func(s userManagementServer, prefix string, victim string) error {
				token := addDestinationTestUser(t, prefix+"last@test.com", "")
				_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: victim})
				return err
			},
		},
		{
			name:   "EditPhoneNumber",
			victim: "+393230031002",
			send: func(s userManagementServer, prefix string, victim string) error {
				token := addDestinationTestUser(t, prefix+"last@test.com", "+393230031102")
				_, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: victim})
				return err
			},
		},
		{
			name:   "ResendContactVerification",
			victim: "+393230031003",
			send: func(s userManagementServer, prefix string, victim string) error {
				token := addDestinationTestUser(t, prefix+"last@test.com", victim)
				_, err := s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
					Token:   &token,
					Address: victim,
					Type:    models.ContactTypePhone,
				})
				return err
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mockWhatsApp := &countingWhatsAppClient{}
			s := newRateLimitTestServer(mockWhatsApp)
			prefix := "daily_cap_" + c.name + "_"

			// Two batches earlier in the day, each moved out of the hour: 19 sends that count
			// for the day and none for the hour.
			clearSendRecords(t)
			sendFromFreshAccounts(t, s, prefix+"early_", c.victim, allowedPhoneDestinationSends)
			moveSendsOutOfTheHour(t)
			sendFromFreshAccounts(t, s, prefix+"mid_", c.victim, allowedPhoneDestinationSendsDaily-allowedPhoneDestinationSends-1)
			moveSendsOutOfTheHour(t)
			if sent := mockWhatsApp.sendsTo(c.victim); sent != allowedPhoneDestinationSendsDaily-1 {
				t.Fatalf("expected %d sends before the last one, got %d", allowedPhoneDestinationSendsDaily-1, sent)
			}

			// The day's last slot.
			if err := c.send(s, prefix+"a_", c.victim); err != nil {
				t.Fatalf("send %d of the day was refused: %v", allowedPhoneDestinationSendsDaily, err)
			}
			// The hour holds one send, far below its limit: only the daily cap refuses this one.
			assertResourceExhausted(t, c.send(s, prefix+"b_", c.victim))

			// The decided cap, written out: changing it is a decision, not a refactor.
			if sent := mockWhatsApp.sendsTo(c.victim); sent != 20 {
				t.Errorf("the number received %d codes in a day, the daily limit is 20", sent)
			}
		})
	}
}
