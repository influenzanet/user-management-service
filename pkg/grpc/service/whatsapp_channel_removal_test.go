package service

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// An opt-out that arrives through WhatsApp (a stop reply, or Meta reporting the number as
// unreachable) removes the whatsapp channel and nothing else: never e-mail added in its place,
// never the phone number. Meta repeats its notifications, so only the first call changes anything.

func removalTestServer(t *testing.T) (*userManagementServer, *auditRecorder, *queueRecorder) {
	t.Helper()
	s := newPhoneTestServer(&countingWhatsAppClient{})
	rec := &auditRecorder{}
	s.clients = &models.APIClients{LoggingService: rec}
	q := &queueRecorder{}
	withQueue(&s, q)
	return &s, rec, q
}

var removalPhoneSeq int64

func removalTestPhone() string {
	return fmt.Sprintf("+3933%07d", 8000000+atomic.AddInt64(&removalPhoneSeq, 1))
}

// unsetChannels stands for a legacy account: no channel list stored.
var unsetChannels []string = nil

// optOutUser creates an account that holds phone verified, with its claim, the way phone
// verification leaves it; then stores the given channels (unsetChannels: none stored) and
// subscriptions to the weekly and the newsletter.
func optOutUser(t *testing.T, accountID, phone string, channels []string) string {
	t.Helper()
	users, err := addTestUsers([]models.User{{
		Account: models.Account{Type: "email", AccountID: accountID, AccountConfirmedAt: time.Now().Unix()},
		ContactInfos: []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone},
		},
		ContactPreferences: models.ContactPreferences{PreferredChannels: []string{models.ChannelEmail}},
		Profiles:           []models.Profile{{ID: primitive.NewObjectID(), MainProfile: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := users[0].ID.Hex()
	if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, id, phone); err != nil {
		t.Fatalf("verify the phone: %v", err)
	}
	setStoredPreferences(t, id, channels)
	return id
}

func setStoredPreferences(t *testing.T, userID string, channels []string) {
	t.Helper()
	_id, _ := primitive.ObjectIDFromHex(userID)
	update := bson.M{"$set": bson.M{
		"contactPreferences.subscribedToWeekly":     true,
		"contactPreferences.subscribedToNewsletter": true,
	}}
	if channels == nil {
		update["$unset"] = bson.M{"contactPreferences.preferredChannels": ""}
	} else {
		update["$set"].(bson.M)["contactPreferences.preferredChannels"] = channels
	}
	if _, err := testUserDBService.GetCollection(testInstanceID, userdb.UserCollection).UpdateOne(context.Background(), bson.M{"_id": _id}, update); err != nil {
		t.Fatal(err)
	}
}

// storedPreferences reads the contact preferences as stored, so that an empty channel list is
// told apart from a missing one (which would read as e-mail).
func storedPreferences(t *testing.T, userID string) bson.M {
	t.Helper()
	_id, _ := primitive.ObjectIDFromHex(userID)
	var doc struct {
		ContactPreferences bson.M `bson:"contactPreferences"`
	}
	if err := testUserDBService.GetCollection(testInstanceID, userdb.UserCollection).FindOne(context.Background(), bson.M{"_id": _id}).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	return doc.ContactPreferences
}

func requireStoredChannels(t *testing.T, userID string, want []string) {
	t.Helper()
	prefs := storedPreferences(t, userID)
	raw, present := prefs["preferredChannels"]
	if want == nil {
		if present {
			t.Errorf("a legacy account got a channel list written: %v", raw)
		}
		return
	}
	got, ok := raw.(bson.A)
	if !present || !ok {
		t.Fatalf("channel list missing or not a list (%v): it would read as e-mail", raw)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("channels = %v, want %v", got, want)
	}
	if prefs["subscribedToWeekly"] != true || prefs["subscribedToNewsletter"] != true {
		t.Errorf("the subscriptions changed: %v", prefs)
	}
}

// requireVerifiedPhone checks that the account still holds the number verified.
func requireVerifiedPhone(t *testing.T, userID, phone string) {
	t.Helper()
	ci, found := phoneOf(t, userID)
	if !found || ci.Phone != phone || ci.ConfirmedAt == 0 {
		t.Errorf("the phone changed: found=%v %+v, want %s verified", found, ci, phone)
	}
}

func byPhone(phone string, reason api.WhatsAppRemovalReason) *api.RemoveWhatsAppChannelReq {
	return &api.RemoveWhatsAppChannelReq{InstanceId: testInstanceID, Target: &api.RemoveWhatsAppChannelReq_PhoneNumber{PhoneNumber: phone}, Reason: reason}
}

func byUser(userID string, reason api.WhatsAppRemovalReason) *api.RemoveWhatsAppChannelReq {
	return &api.RemoveWhatsAppChannelReq{InstanceId: testInstanceID, Target: &api.RemoveWhatsAppChannelReq_UserId{UserId: userID}, Reason: reason}
}

const (
	stopKeyword   = api.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_STOP_KEYWORD
	undeliverable = api.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_UNDELIVERABLE
)

func requireRemoved(t *testing.T, s *userManagementServer, req *api.RemoveWhatsAppChannelReq, want bool) {
	t.Helper()
	resp, err := s.RemoveWhatsAppChannel(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ChannelRemoved != want {
		t.Fatalf("channel_removed = %v, want %v", resp.ChannelRemoved, want)
	}
}

func TestRemoveWhatsAppChannelByPhoneNumber(t *testing.T) {
	t.Run("whatsapp goes, e-mail and the phone stay; audit and cancel once", func(t *testing.T) {
		s, rec, q := removalTestServer(t)
		phone := removalTestPhone()
		id := optOutUser(t, "wa_remove_phone@test.com", phone, []string{models.ChannelEmail, models.ChannelWhatsApp})

		// Spelled differently from the stored E.164 form: the number is normalised first.
		spelled := "0039 " + phone[3:6] + " " + phone[6:]
		requireRemoved(t, s, byPhone(spelled, stopKeyword), true)

		requireStoredChannels(t, id, []string{models.ChannelEmail})
		requireVerifiedPhone(t, id, phone)
		requireAudit(t, rec.only(t), id, LOG_EVENT_WHATSAPP_CHANNEL_REMOVED, logEv,
			"outcome=removed reason=stop_keyword phone="+masked(phone)+" from=[email whatsapp] to=[email]", phone)
		requireCancelled(t, s, q, id, false, nil)

		// Meta sends the same notification again: nothing more happens.
		requireRemoved(t, s, byPhone(phone, stopKeyword), false)
		requireStoredChannels(t, id, []string{models.ChannelEmail})
		rec.none(t)
		requireCancelled(t, s, q, id, false, nil)
	})

	t.Run("a whatsapp-only participant is left with no channel, never e-mail", func(t *testing.T) {
		s, rec, _ := removalTestServer(t)
		phone := removalTestPhone()
		id := optOutUser(t, "wa_remove_only@test.com", phone, []string{models.ChannelWhatsApp})

		requireRemoved(t, s, byPhone(phone, stopKeyword), true)

		requireStoredChannels(t, id, []string{})
		requireVerifiedPhone(t, id, phone)
		requireAudit(t, rec.only(t), id, LOG_EVENT_WHATSAPP_CHANNEL_REMOVED, logEv,
			"outcome=removed reason=stop_keyword phone="+masked(phone)+" from=[whatsapp] to=[]", phone)
	})

	t.Run("nobody holds the number: nothing changes", func(t *testing.T) {
		s, rec, q := removalTestServer(t)
		requireRemoved(t, s, byPhone(removalTestPhone(), stopKeyword), false)
		rec.none(t)
		requireNoCancel(t, s, q)
	})

	t.Run("a number only pending verification matches no account", func(t *testing.T) {
		s, rec, q := removalTestServer(t)
		phone := removalTestPhone()
		// The account carries the number unverified, with whatsapp among its stored channels,
		// and a claim names it (left over from an earlier verification of the same number).
		id := cancelTestUser(t, "wa_remove_pending@test.com", whatsAppAndEmail).Id
		_id, _ := primitive.ObjectIDFromHex(id)
		if _, err := testUserDBService.GetCollection(testInstanceID, userdb.UserCollection).UpdateOne(context.Background(),
			bson.M{"_id": _id, "contactInfos.type": models.ContactTypePhone},
			bson.M{"$set": bson.M{"contactInfos.$.phone": phone, "contactInfos.$.confirmedAt": 0}}); err != nil {
			t.Fatal(err)
		}
		if _, err := testUserDBService.GetCollection(testInstanceID, userdb.VerifiedPhoneCollection).InsertOne(context.Background(),
			bson.M{"_id": phone, "userId": _id, "claimedAt": time.Now().Unix(), "rev": 0}); err != nil {
			t.Fatal(err)
		}

		requireRemoved(t, s, byPhone(phone, stopKeyword), false)

		if got := channelsOf(t, id); !hasChannel(got, models.ChannelWhatsApp) {
			t.Errorf("whatsapp was removed from an account that does not hold the number verified: %v", got)
		}
		rec.none(t)
		requireNoCancel(t, s, q)
	})

	t.Run("the account holding the number has no whatsapp: nothing changes", func(t *testing.T) {
		s, rec, q := removalTestServer(t)
		phone := removalTestPhone()
		id := optOutUser(t, "wa_remove_email_only@test.com", phone, []string{models.ChannelEmail})

		requireRemoved(t, s, byPhone(phone, stopKeyword), false)

		requireStoredChannels(t, id, []string{models.ChannelEmail})
		rec.none(t)
		requireNoCancel(t, s, q)
	})
}

func TestRemoveWhatsAppChannelByUserID(t *testing.T) {
	t.Run("undeliverable: whatsapp goes, the audit names the account's number masked", func(t *testing.T) {
		s, rec, q := removalTestServer(t)
		phone := removalTestPhone()
		id := optOutUser(t, "wa_remove_user@test.com", phone, []string{models.ChannelWhatsApp, models.ChannelEmail})

		requireRemoved(t, s, byUser(id, undeliverable), true)

		requireStoredChannels(t, id, []string{models.ChannelEmail})
		requireVerifiedPhone(t, id, phone)
		requireAudit(t, rec.only(t), id, LOG_EVENT_WHATSAPP_CHANNEL_REMOVED, logEv,
			"outcome=removed reason=undeliverable phone="+masked(phone)+" from=[whatsapp email] to=[email]", phone)
		requireCancelled(t, s, q, id, false, nil)

		requireRemoved(t, s, byUser(id, undeliverable), false)
		rec.none(t)
		requireCancelled(t, s, q, id, false, nil)
	})

	t.Run("no such account", func(t *testing.T) {
		s, rec, q := removalTestServer(t)
		requireRemoved(t, s, byUser(primitive.NewObjectID().Hex(), undeliverable), false)
		rec.none(t)
		requireNoCancel(t, s, q)
	})

	t.Run("a legacy account reads as e-mail and is left as it is", func(t *testing.T) {
		s, rec, q := removalTestServer(t)
		id := optOutUser(t, "wa_remove_legacy@test.com", removalTestPhone(), unsetChannels)

		requireRemoved(t, s, byUser(id, undeliverable), false)

		requireStoredChannels(t, id, nil)
		rec.none(t)
		requireNoCancel(t, s, q)
	})
}

func TestRemoveWhatsAppChannelRefusals(t *testing.T) {
	s, rec, q := removalTestServer(t)
	phone := removalTestPhone()
	id := optOutUser(t, "wa_remove_refusals@test.com", phone, []string{models.ChannelEmail, models.ChannelWhatsApp})

	for _, c := range []struct {
		name string
		req  *api.RemoveWhatsAppChannelReq
	}{
		{"no request", nil},
		{"no instance", &api.RemoveWhatsAppChannelReq{Target: &api.RemoveWhatsAppChannelReq_UserId{UserId: id}, Reason: stopKeyword}},
		{"no target", &api.RemoveWhatsAppChannelReq{InstanceId: testInstanceID, Reason: stopKeyword}},
		{"no reason", byUser(id, api.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_UNSPECIFIED)},
		{"a reason the contract does not know", byUser(id, api.WhatsAppRemovalReason(99))},
		{"a user id that is not one", byUser("not-an-id", stopKeyword)},
		{"a number that is not one", byPhone("not-a-number", stopKeyword)},
		// Meta writes numbers without "+": the caller adds it, a bare string of digits is not
		// guessed into a country.
		{"digits without the international prefix", byPhone(strings.TrimPrefix(phone, "+"), stopKeyword)},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.RemoveWhatsAppChannel(context.Background(), c.req)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument, got %v", err)
			}
		})
	}
	requireStoredChannels(t, id, []string{models.ChannelEmail, models.ChannelWhatsApp})
	rec.none(t)
	requireNoCancel(t, s, q)
}

// Concurrent deliveries of the same opt-out: exactly one removes the channel, so the audit
// event and the cancellation happen once.
func TestRemoveWhatsAppChannelConcurrentDuplicates(t *testing.T) {
	s, rec, q := removalTestServer(t)
	phone := removalTestPhone()
	id := optOutUser(t, "wa_remove_concurrent@test.com", phone, []string{models.ChannelEmail, models.ChannelWhatsApp})

	const callers = 8
	var removed int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			req := byPhone(phone, stopKeyword)
			if i%2 == 1 {
				req = byUser(id, stopKeyword)
			}
			resp, err := s.RemoveWhatsAppChannel(context.Background(), req)
			if err != nil {
				t.Errorf("call %d: %v", i, err)
				return
			}
			if resp.ChannelRemoved {
				atomic.AddInt32(&removed, 1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if removed != 1 {
		t.Errorf("%d calls reported the removal, want exactly 1", removed)
	}
	requireStoredChannels(t, id, []string{models.ChannelEmail})
	rec.only(t)
	requireCancelled(t, s, q, id, false, nil)
}

func TestRemoveWhatsAppChannelFailures(t *testing.T) {
	t.Run("the database is unreachable", func(t *testing.T) {
		for _, req := range []*api.RemoveWhatsAppChannelReq{
			byPhone(removalTestPhone(), stopKeyword),
			byUser(primitive.NewObjectID().Hex(), undeliverable),
		} {
			s, rec, q := removalTestServer(t)
			db := newMonitoredUserDB(t, newFindBarrier())
			if err := db.DBClient.Disconnect(context.Background()); err != nil {
				t.Fatal(err)
			}
			s.userDBservice = db

			_, err := s.RemoveWhatsAppChannel(context.Background(), req)
			if status.Code(err) != codes.Internal {
				t.Fatalf("expected Internal, got %v", err)
			}
			if msg := status.Convert(err).Message(); strings.Contains(msg, "disconnected") {
				t.Errorf("the database error reached the caller: %q", msg)
			}
			rec.none(t)
			requireNoCancel(t, s, q)
		}
	})

	t.Run("messaging is down: the channel is still removed", func(t *testing.T) {
		s, _, q := removalTestServer(t)
		q.failures = 100
		phone := removalTestPhone()
		id := optOutUser(t, "wa_remove_messaging_down@test.com", phone, []string{models.ChannelEmail, models.ChannelWhatsApp})

		original := logger.Error.Writer()
		logger.Error.SetOutput(&bytes.Buffer{})
		defer logger.Error.SetOutput(original)

		requireRemoved(t, s, byPhone(phone, stopKeyword), true)
		s.whatsAppQueue.Wait()
		requireStoredChannels(t, id, []string{models.ChannelEmail})
	})
}

// The number an opt-out came from is logged masked, whatever the outcome.
func TestRemoveWhatsAppChannelLogsNoFullNumber(t *testing.T) {
	s, _, _ := removalTestServer(t)
	held := removalTestPhone()
	optOutUser(t, "wa_remove_logs@test.com", held, []string{models.ChannelWhatsApp})
	unknown := removalTestPhone()

	var buf bytes.Buffer
	for _, l := range []*log.Logger{logger.Info, logger.Error, logger.Debug, logger.Warning} {
		original := l.Writer()
		l.SetOutput(&buf)
		defer l.SetOutput(original)
	}
	requireRemoved(t, s, byPhone(held, stopKeyword), true)
	requireRemoved(t, s, byPhone(held, stopKeyword), false)
	requireRemoved(t, s, byPhone(unknown, stopKeyword), false)
	s.whatsAppQueue.Wait()

	logged := buf.String()
	for _, phone := range []string{held, unknown} {
		if strings.Contains(logged, phone[1:]) {
			t.Errorf("the logs carry the full number %s: %q", phone, logged)
		}
	}
	if !strings.Contains(logged, "phone="+masked(unknown)+" outcome=no_account") {
		t.Errorf("the unmatched opt-out is not logged with the masked number: %q", logged)
	}
}
