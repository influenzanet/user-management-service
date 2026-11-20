package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/influenzanet/go-utils/pkg/constants"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The unsubscribe link of a message turns one subscription off without a login. There is one
// token per participant, so the same token turns off the newsletter or the weekly reminder.

func unsubscribeTestServer(t *testing.T) (*userManagementServer, *auditRecorder, *queueRecorder) {
	t.Helper()
	s := userManagementServer{userDBservice: testUserDBService, globalDBService: testGlobalDBService}
	rec := &auditRecorder{}
	s.clients = &models.APIClients{LoggingService: rec}
	q := &queueRecorder{}
	withQueue(&s, q)
	return &s, rec, q
}

var unsubscribeUserSeq int64

// unsubscribeTestUser creates a participant with the given subscriptions and returns their ID.
func unsubscribeTestUser(t *testing.T, newsletter, weekly bool) string {
	t.Helper()
	accountID := fmt.Sprintf("unsubscribe_with_token_%d@test.com", atomic.AddInt64(&unsubscribeUserSeq, 1))
	users, err := addTestUsers([]models.User{{
		Account: models.Account{Type: "email", AccountID: accountID, AccountConfirmedAt: time.Now().Unix()},
		ContactInfos: []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
		},
		ContactPreferences: models.ContactPreferences{
			PreferredChannels:      []string{models.ChannelEmail, models.ChannelWhatsApp},
			SubscribedToNewsletter: newsletter,
			SubscribedToWeekly:     weekly,
		},
		Profiles: []models.Profile{{ID: primitive.NewObjectID(), MainProfile: true}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return users[0].ID.Hex()
}

func unsubscribeTestToken(t *testing.T, userID, purpose string, expiration int64) string {
	t.Helper()
	token, err := testGlobalDBService.AddTempToken(models.TempToken{
		InstanceID: testInstanceID, UserID: userID, Purpose: purpose, Expiration: expiration,
	})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func subscriptionsOf(t *testing.T, userID string) (newsletter, weekly bool) {
	t.Helper()
	user, err := testUserDBService.GetUserByID(testInstanceID, userID)
	if err != nil {
		t.Fatal(err)
	}
	return user.ContactPreferences.SubscribedToNewsletter, user.ContactPreferences.SubscribedToWeekly
}

func TestUnsubscribeWithToken(t *testing.T) {
	cases := []struct {
		subscription api.Subscription
		name         string
		messageType  string
	}{
		{api.Subscription_SUBSCRIPTION_NEWSLETTER, "newsletter", constants.EMAIL_TYPE_NEWSLETTER},
		{api.Subscription_SUBSCRIPTION_WEEKLY, "weekly", constants.EMAIL_TYPE_WEEKLY},
	}
	for _, c := range cases {
		t.Run(c.name+", subscribed to both", func(t *testing.T) {
			s, rec, q := unsubscribeTestServer(t)
			userID := unsubscribeTestUser(t, true, true)
			token := unsubscribeTestToken(t, userID, constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER, time.Now().Unix()+600)

			resp, err := s.UnsubscribeWithToken(context.Background(), &api.UnsubscribeWithTokenReq{Token: token, Subscription: c.subscription})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Status != api.ServiceStatus_NORMAL {
				t.Errorf("unexpected status %v", resp.Status)
			}
			newsletter, weekly := subscriptionsOf(t, userID)
			if wantNewsletter := c.subscription != api.Subscription_SUBSCRIPTION_NEWSLETTER; newsletter != wantNewsletter {
				t.Errorf("newsletter: got %v, want %v", newsletter, wantNewsletter)
			}
			if wantWeekly := c.subscription != api.Subscription_SUBSCRIPTION_WEEKLY; weekly != wantWeekly {
				t.Errorf("weekly: got %v, want %v", weekly, wantWeekly)
			}
			requireCancelled(t, s, q, userID, false, []string{c.messageType})
			requireAudit(t, rec.only(t), userID, LOG_EVENT_UNSUBSCRIBE_LINK, logEv, "subscription="+c.name+" outcome=unsubscribed")
		})

		t.Run(c.name+", already off", func(t *testing.T) {
			s, rec, q := unsubscribeTestServer(t)
			userID := unsubscribeTestUser(t, false, false)
			token := unsubscribeTestToken(t, userID, constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER, time.Now().Unix()+600)

			if _, err := s.UnsubscribeWithToken(context.Background(), &api.UnsubscribeWithTokenReq{Token: token, Subscription: c.subscription}); err != nil {
				t.Fatal(err)
			}
			if newsletter, weekly := subscriptionsOf(t, userID); newsletter || weekly {
				t.Errorf("a subscription was turned on: newsletter=%v weekly=%v", newsletter, weekly)
			}
			requireNoCancel(t, s, q)
			requireAudit(t, rec.only(t), userID, LOG_EVENT_UNSUBSCRIBE_LINK, logEv, "subscription="+c.name+" outcome=not_subscribed")
		})
	}

	t.Run("the same link twice", func(t *testing.T) {
		s, rec, q := unsubscribeTestServer(t)
		userID := unsubscribeTestUser(t, true, true)
		token := unsubscribeTestToken(t, userID, constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER, time.Now().Unix()+600)
		req := &api.UnsubscribeWithTokenReq{Token: token, Subscription: api.Subscription_SUBSCRIPTION_WEEKLY}
		if _, err := s.UnsubscribeWithToken(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		requireCancelled(t, s, q, userID, false, []string{constants.EMAIL_TYPE_WEEKLY})
		rec.only(t)
		if _, err := s.UnsubscribeWithToken(context.Background(), req); err != nil {
			t.Fatalf("the second use of the token failed: %v", err)
		}
		requireCancelled(t, s, q, userID, false, []string{constants.EMAIL_TYPE_WEEKLY})
		requireAudit(t, rec.only(t), userID, LOG_EVENT_UNSUBSCRIBE_LINK, logEv, "subscription=weekly outcome=not_subscribed")
	})
}

// The older endpoint is the newsletter case of the new one, audit event included.
func TestUseUnsubscribeTokenIsTheNewsletter(t *testing.T) {
	s, rec, q := unsubscribeTestServer(t)
	userID := unsubscribeTestUser(t, true, true)
	token := unsubscribeTestToken(t, userID, constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER, time.Now().Unix()+600)
	if _, err := s.UseUnsubscribeToken(context.Background(), &api.TempToken{Token: token}); err != nil {
		t.Fatal(err)
	}
	if newsletter, weekly := subscriptionsOf(t, userID); newsletter || !weekly {
		t.Errorf("got newsletter=%v weekly=%v, want false true", newsletter, weekly)
	}
	requireCancelled(t, s, q, userID, false, []string{constants.EMAIL_TYPE_NEWSLETTER})
	requireAudit(t, rec.only(t), userID, LOG_EVENT_UNSUBSCRIBE_LINK, logEv, "subscription=newsletter outcome=unsubscribed")
}

// A refused request changes nothing, cancels nothing and records nothing.
func TestUnsubscribeWithTokenRefusals(t *testing.T) {
	s, rec, q := unsubscribeTestServer(t)
	userID := unsubscribeTestUser(t, true, true)
	valid := unsubscribeTestToken(t, userID, constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER, time.Now().Unix()+600)

	cases := []struct {
		name string
		req  *api.UnsubscribeWithTokenReq
		msg  string
	}{
		{"no request", nil, "missing argument"},
		{"no token", &api.UnsubscribeWithTokenReq{Subscription: api.Subscription_SUBSCRIPTION_WEEKLY}, "missing argument"},
		{"no subscription", &api.UnsubscribeWithTokenReq{Token: valid}, "unknown subscription"},
		{"unknown subscription", &api.UnsubscribeWithTokenReq{Token: valid, Subscription: api.Subscription(99)}, "unknown subscription"},
		{"unknown token", &api.UnsubscribeWithTokenReq{Token: "wrong", Subscription: api.Subscription_SUBSCRIPTION_WEEKLY}, "wrong token"},
		{"token of another purpose", &api.UnsubscribeWithTokenReq{
			Token:        unsubscribeTestToken(t, userID, constants.TOKEN_PURPOSE_SURVEY_LOGIN, time.Now().Unix()+600),
			Subscription: api.Subscription_SUBSCRIPTION_WEEKLY,
		}, "wrong token purpose"},
		{"expired token", &api.UnsubscribeWithTokenReq{
			Token:        unsubscribeTestToken(t, userID, constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER, time.Now().Unix()-1),
			Subscription: api.Subscription_SUBSCRIPTION_WEEKLY,
		}, "token expired"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := s.UnsubscribeWithToken(context.Background(), c.req)
			if st := status.Convert(err); st.Code() != codes.InvalidArgument || st.Message() != c.msg {
				t.Errorf("got %v, want InvalidArgument %q", err, c.msg)
			}
			if newsletter, weekly := subscriptionsOf(t, userID); !newsletter || !weekly {
				t.Errorf("a subscription changed: newsletter=%v weekly=%v", newsletter, weekly)
			}
			requireNoCancel(t, s, q)
			rec.none(t)
		})
	}

	t.Run("the old endpoint refuses a token of another purpose", func(t *testing.T) {
		token := unsubscribeTestToken(t, userID, constants.TOKEN_PURPOSE_SURVEY_LOGIN, time.Now().Unix()+600)
		_, err := s.UseUnsubscribeToken(context.Background(), &api.TempToken{Token: token})
		if st := status.Convert(err); st.Code() != codes.InvalidArgument || st.Message() != "wrong token purpose" {
			t.Errorf("got %v, want InvalidArgument \"wrong token purpose\"", err)
		}
		if newsletter, _ := subscriptionsOf(t, userID); !newsletter {
			t.Error("the newsletter was turned off")
		}
		requireNoCancel(t, s, q)
		rec.none(t)
	})
}

// The account is gone between the read and the write: the request fails, an error event is
// recorded, and nothing is cancelled.
func TestUnsubscribeWithTokenWriteFails(t *testing.T) {
	userID := unsubscribeTestUser(t, true, true)
	token := unsubscribeTestToken(t, userID, constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER, time.Now().Unix()+600)
	barrier := newFindBarrier()
	s, rec, q := unsubscribeTestServer(t)
	s.userDBservice = newMonitoredUserDB(t, barrier)

	err := runErrorWhileSnapshotBlocked(t, barrier, func() error {
		_, err := s.UnsubscribeWithToken(context.Background(), &api.UnsubscribeWithTokenReq{Token: token, Subscription: api.Subscription_SUBSCRIPTION_WEEKLY})
		return err
	}, func() {
		if err := testUserDBService.DeleteUser(testInstanceID, userID); err != nil {
			t.Fatalf("delete the account: %v", err)
		}
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("got %v, want Internal", err)
	}
	requireNoCancel(t, s, q)
	requireAudit(t, rec.only(t), userID, LOG_EVENT_UNSUBSCRIBE_LINK, errEv, "subscription=weekly outcome=error")
}

// The weekly path writes its own flag only, like the newsletter path
// (TestUnsubscribePreservesConcurrentPhoneFinalizationAndDeletion).
func TestUnsubscribeWeeklyPreservesConcurrentPhoneFinalizationAndDeletion(t *testing.T) {
	for index, mutation := range preferenceMutationCases() {
		t.Run(mutation.name, func(t *testing.T) {
			setup := mutation.setup
			mutation.setup = func(user *models.User) {
				setup(user)
				user.ContactPreferences.SubscribedToNewsletter = true
				user.ContactPreferences.SubscribedToWeekly = true
			}
			seeded := seedIsolationUser(t, fmt.Sprintf("isolation-unsubscribe-weekly-%02d@test.com", index), "unused-password", mutation)
			token := isolationTempToken(t, seeded.ID.Hex(), constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER)
			barrier := newFindBarrier()
			server := userManagementServer{
				userDBservice:   newMonitoredUserDB(t, barrier),
				globalDBService: testGlobalDBService,
			}

			var want models.User
			err := runErrorWhileSnapshotBlocked(t, barrier, func() error {
				_, err := server.UnsubscribeWithToken(context.Background(), &api.UnsubscribeWithTokenReq{Token: token, Subscription: api.Subscription_SUBSCRIPTION_WEEKLY})
				return err
			}, func() {
				mutation.run(t, seeded.ID.Hex())
				var readErr error
				want, readErr = testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
				if readErr != nil {
					t.Fatalf("read state after concurrent mutation: %v", readErr)
				}
			})
			if err != nil {
				t.Fatalf("UnsubscribeWithToken failed: %v", err)
			}
			got, err := testUserDBService.GetUserByID(testInstanceID, seeded.ID.Hex())
			if err != nil {
				t.Fatalf("read state after unsubscribe: %v", err)
			}
			assertProtectedDBState(t, got, want)
			if got.ContactPreferences.SubscribedToWeekly {
				t.Error("unsubscribe did not persist its intended subscription change")
			}
			if !got.ContactPreferences.SubscribedToNewsletter {
				t.Error("the weekly unsubscribe turned the newsletter off")
			}
		})
	}
}
