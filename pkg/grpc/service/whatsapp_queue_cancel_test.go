package service

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coneno/logger"
	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"github.com/influenzanet/user-management-service/pkg/whatsappqueue"
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Every path that withdraws WhatsApp from a participant asks messaging to cancel what is
// queued for them, with bounded retries, and the participant's action never fails because of
// messaging.

// queueRecorder stands in for messaging's DeleteOutgoingWhatsAppForUser. failures is how many
// calls fail (Unavailable) before the next ones succeed; every other method is left nil.
type queueRecorder struct {
	messageAPI.MessagingServiceApiClient

	mu       sync.Mutex
	failures int
	requests []*messageAPI.DeleteOutgoingWhatsAppForUserReq
}

func (q *queueRecorder) DeleteOutgoingWhatsAppForUser(_ context.Context, in *messageAPI.DeleteOutgoingWhatsAppForUserReq, _ ...grpc.CallOption) (*messageAPI.DeleteOutgoingWhatsAppForUserResp, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.requests = append(q.requests, in)
	if q.failures > 0 {
		q.failures--
		return nil, status.Error(codes.Unavailable, "messaging is down")
	}
	return &messageAPI.DeleteOutgoingWhatsAppForUserResp{}, nil
}

func (q *queueRecorder) calls() []*messageAPI.DeleteOutgoingWhatsAppForUserReq {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]*messageAPI.DeleteOutgoingWhatsAppForUserReq(nil), q.requests...)
}

// withQueue gives the server a canceller over the recorder, with short waits between attempts.
func withQueue(s *userManagementServer, q *queueRecorder) {
	s.whatsAppQueue = whatsappqueue.NewCanceller(q)
	s.whatsAppQueue.Backoff = []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}
	s.whatsAppQueue.AttemptTimeout = time.Second
}

// requireCancelled waits for the background cancellations and checks they are exactly the
// expected ones: one per entry, for userID, with the given message types (nil: every type).
func requireCancelled(t *testing.T, s *userManagementServer, q *queueRecorder, userID string, purge bool, messageTypes ...[]string) {
	t.Helper()
	s.whatsAppQueue.Wait()
	got := q.calls()
	if len(got) != len(messageTypes) {
		t.Fatalf("expected %d cancellation(s), got %d: %v", len(messageTypes), len(got), got)
	}
	for i, want := range messageTypes {
		req := got[i]
		if req.UserId != userID || req.InstanceId != testInstanceID {
			t.Errorf("cancellation %d is for %s/%s, want %s/%s", i, req.InstanceId, req.UserId, testInstanceID, userID)
		}
		if req.PurgeArchive != purge {
			t.Errorf("cancellation %d: purgeArchive=%v, want %v", i, req.PurgeArchive, purge)
		}
		if !(len(want) == 0 && len(req.MessageTypes) == 0) && !reflect.DeepEqual(req.MessageTypes, want) {
			t.Errorf("cancellation %d: message types %v, want %v", i, req.MessageTypes, want)
		}
	}
}

func requireNoCancel(t *testing.T, s *userManagementServer, q *queueRecorder) {
	t.Helper()
	s.whatsAppQueue.Wait()
	if got := q.calls(); len(got) != 0 {
		t.Errorf("expected no cancellation, got %v", got)
	}
}

// cancelTestServer is a server whose messaging and logging accept anything, plus a queue recorder.
func cancelTestServer(t *testing.T) (*userManagementServer, *queueRecorder) {
	t.Helper()
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)
	messaging := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
	messaging.EXPECT().SendInstantEmail(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	logging := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)
	logging.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	s := newRateLimitTestServer(&countingWhatsAppClient{})
	s.Intervals.MaxVerificationAttempts = 3
	s.clients = &models.APIClients{MessagingService: messaging, LoggingService: logging}
	q := &queueRecorder{}
	withQueue(&s, q)
	return &s, q
}

var cancelPhoneSeq int64

func cancelTestPhone() string {
	return fmt.Sprintf("+3933%07d", 6000000+atomic.AddInt64(&cancelPhoneSeq, 1))
}

// cancelTestUser creates an account with a verified phone and the given preferences.
func cancelTestUser(t *testing.T, accountID string, prefs models.ContactPreferences) *api_types.TokenInfos {
	t.Helper()
	users, err := addTestUsers([]models.User{{
		Account: models.Account{Type: "email", AccountID: accountID, AccountConfirmedAt: time.Now().Unix()},
		ContactInfos: []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: cancelTestPhone(), ConfirmedAt: time.Now().Unix()},
		},
		ContactPreferences: prefs,
		Profiles:           []models.Profile{{ID: primitive.NewObjectID(), MainProfile: true}},
	}})
	if err != nil {
		t.Fatalf("failed to create test user: %s", err.Error())
	}
	return &api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}
}

var whatsAppAndEmail = models.ContactPreferences{PreferredChannels: []string{models.ChannelEmail, models.ChannelWhatsApp}}

func TestDeleteAccountPurgesTheUsersWhatsApp(t *testing.T) {
	s, q := cancelTestServer(t)
	token := cancelTestUser(t, "wa_cancel_delete_account@test.com", whatsAppAndEmail)
	if _, err := s.DeleteAccount(context.Background(), &api.UserReference{Token: token, UserId: token.Id}); err != nil {
		t.Fatal(err)
	}
	requireCancelled(t, s, q, token.Id, true, nil)
}

func TestDeleteAccountThatFailsCancelsNothing(t *testing.T) {
	s, q := cancelTestServer(t)
	token := &api_types.TokenInfos{Id: primitive.NewObjectID().Hex(), InstanceId: testInstanceID}
	if _, err := s.DeleteAccount(context.Background(), &api.UserReference{Token: token, UserId: token.Id}); err == nil {
		t.Fatal("expected the deletion of a missing account to fail")
	}
	requireNoCancel(t, s, q)
}

func TestDeletePhoneNumberCancelsEveryType(t *testing.T) {
	s, q := cancelTestServer(t)
	token := cancelTestUser(t, "wa_cancel_delete_phone@test.com", whatsAppAndEmail)
	if _, err := s.DeletePhoneNumber(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	requireCancelled(t, s, q, token.Id, false, nil)
}

func TestEditPhoneNumberCancelsWhenTheNumberChanges(t *testing.T) {
	t.Run("a new number", func(t *testing.T) {
		s, q := cancelTestServer(t)
		token := cancelTestUser(t, "wa_cancel_edit_phone@test.com", whatsAppAndEmail)
		if _, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: token, NewPhone: cancelTestPhone()}); err != nil {
			t.Fatal(err)
		}
		requireCancelled(t, s, q, token.Id, false, nil)
	})

	t.Run("a resend to the same pending number changes nothing", func(t *testing.T) {
		s, q := cancelTestServer(t)
		token := addVerifyCodeTestUser(t, "wa_cancel_edit_resend@test.com", cancelTestPhone(), models.VerificationCode{}, nil, nil)
		phone, _ := phoneOf(t, token.Id)
		if _, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: &token, NewPhone: phone.Phone}); err != nil {
			t.Fatal(err)
		}
		requireNoCancel(t, s, q)
	})

	t.Run("a refused change cancels nothing", func(t *testing.T) {
		s, q := cancelTestServer(t)
		token := cancelTestUser(t, "wa_cancel_edit_refused@test.com", whatsAppAndEmail)
		if _, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: token, NewPhone: "not-a-number"}); err == nil {
			t.Fatal("expected a refusal")
		}
		requireNoCancel(t, s, q)
	})
}

func TestTooManyWrongCodesCancels(t *testing.T) {
	t.Run("the removal of the phone", func(t *testing.T) {
		s, q := cancelTestServer(t)
		exhausted := validCode("123456")
		exhausted.Attempts = 3
		token := addVerifyCodeTestUser(t, "wa_cancel_exhausted@test.com", cancelTestPhone(), exhausted, nil, nil)
		if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "000000"}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied, got %v", err)
		}
		requireCancelled(t, s, q, token.Id, false, nil)
	})

	t.Run("a wrong code that is not the last, or a right one, cancels nothing", func(t *testing.T) {
		s, q := cancelTestServer(t)
		token := addVerifyCodeTestUser(t, "wa_cancel_wrong_once@test.com", cancelTestPhone(), validCode("123456"), nil, nil)
		if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "000000"}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied, got %v", err)
		}
		if _, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: &token, Code: "123456"}); err != nil {
			t.Fatal(err)
		}
		requireNoCancel(t, s, q)
	})
}

func TestUpdateContactPreferencesCancelsWhatIsWithdrawn(t *testing.T) {
	both := []string{models.ChannelEmail, models.ChannelWhatsApp}
	cases := []struct {
		name   string
		before models.ContactPreferences
		after  *api.ContactPreferences
		want   [][]string // one entry per expected cancellation; nil entry = every type
	}{
		{"whatsapp leaves the channels",
			models.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true},
			&api.ContactPreferences{PreferredChannels: []string{models.ChannelEmail}, SubscribedToWeekly: true},
			[][]string{nil}},
		{"no channel at all",
			models.ContactPreferences{PreferredChannels: []string{models.ChannelWhatsApp}, SubscribedToWeekly: true},
			&api.ContactPreferences{PreferredChannels: []string{}, SubscribedToWeekly: true},
			[][]string{nil}},
		{"whatsapp leaves and the weekly goes too: every type, once",
			models.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true},
			&api.ContactPreferences{PreferredChannels: []string{models.ChannelEmail}},
			[][]string{nil}},
		{"the weekly is turned off",
			models.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true, SubscribedToNewsletter: true},
			&api.ContactPreferences{PreferredChannels: both, SubscribedToNewsletter: true},
			[][]string{{constants.EMAIL_TYPE_WEEKLY}}},
		{"the newsletter is turned off",
			models.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true, SubscribedToNewsletter: true},
			&api.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true},
			[][]string{{constants.EMAIL_TYPE_NEWSLETTER}}},
		{"both subscriptions are turned off",
			models.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true, SubscribedToNewsletter: true},
			&api.ContactPreferences{PreferredChannels: both},
			[][]string{{constants.EMAIL_TYPE_WEEKLY, constants.EMAIL_TYPE_NEWSLETTER}}},
		{"nothing withdrawn",
			models.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true},
			&api.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true},
			nil},
		{"whatsapp added, subscriptions turned on",
			models.ContactPreferences{PreferredChannels: []string{models.ChannelEmail}},
			&api.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true, SubscribedToNewsletter: true},
			nil},
		{"e-mail removed, whatsapp kept",
			models.ContactPreferences{PreferredChannels: both, SubscribedToWeekly: true},
			&api.ContactPreferences{PreferredChannels: []string{models.ChannelWhatsApp}, SubscribedToWeekly: true},
			nil},
		// A legacy account reads as e-mail: unticking e-mail withdraws nothing from WhatsApp.
		{"legacy account, e-mail removed",
			models.ContactPreferences{SubscribedToWeekly: true},
			&api.ContactPreferences{PreferredChannels: []string{}, SubscribedToWeekly: true},
			nil},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, q := cancelTestServer(t)
			token := cancelTestUser(t, fmt.Sprintf("wa_cancel_prefs_%d@test.com", i), c.before)
			if _, err := s.UpdateContactPreferences(context.Background(), &api.ContactPreferencesMsg{Token: token, ContactPreferences: c.after}); err != nil {
				t.Fatal(err)
			}
			if c.want == nil {
				requireNoCancel(t, s, q)
				return
			}
			requireCancelled(t, s, q, token.Id, false, c.want...)
		})
	}

	t.Run("a refused update cancels nothing", func(t *testing.T) {
		s, q := cancelTestServer(t)
		token := cancelTestUser(t, "wa_cancel_prefs_refused@test.com", whatsAppAndEmail)
		if _, err := s.UpdateContactPreferences(context.Background(), &api.ContactPreferencesMsg{
			Token: token, ContactPreferences: &api.ContactPreferences{PreferredChannels: []string{"telegram"}},
		}); err == nil {
			t.Fatal("expected a refusal")
		}
		requireNoCancel(t, s, q)
	})
}

func TestUseUnsubscribeTokenCancelsTheNewsletter(t *testing.T) {
	for _, subscribed := range []bool{true, false} {
		t.Run(fmt.Sprintf("subscribed=%v", subscribed), func(t *testing.T) {
			s, q := cancelTestServer(t)
			token := cancelTestUser(t, fmt.Sprintf("wa_cancel_unsubscribe_%v@test.com", subscribed), models.ContactPreferences{
				PreferredChannels: []string{models.ChannelWhatsApp}, SubscribedToNewsletter: subscribed, SubscribedToWeekly: true,
			})
			tempToken, err := s.globalDBService.AddTempToken(models.TempToken{
				InstanceID: testInstanceID, UserID: token.Id, Purpose: constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER,
				Expiration: time.Now().Unix() + 600,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UseUnsubscribeToken(context.Background(), &api.TempToken{Token: tempToken}); err != nil {
				t.Fatal(err)
			}
			if !subscribed {
				requireNoCancel(t, s, q)
				return
			}
			requireCancelled(t, s, q, token.Id, false, []string{constants.EMAIL_TYPE_NEWSLETTER})
		})
	}
}

// Messaging down: the participant's action still succeeds. Down for a moment, the cancellation
// gets through on a later attempt; down throughout, an alert names the user and nothing else.
func TestWithdrawalWhenMessagingIsDown(t *testing.T) {
	captureAlerts := func(t *testing.T, fn func()) string {
		t.Helper()
		var buf bytes.Buffer
		original := logger.Error.Writer()
		logger.Error.SetOutput(&buf)
		defer logger.Error.SetOutput(original)
		fn()
		return buf.String()
	}

	t.Run("down, then up: retried", func(t *testing.T) {
		s, q := cancelTestServer(t)
		q.failures = 2
		token := cancelTestUser(t, "wa_cancel_down_up@test.com", whatsAppAndEmail)
		logged := captureAlerts(t, func() {
			user, err := s.DeletePhoneNumber(context.Background(), token)
			if err != nil {
				t.Fatalf("the participant's action failed: %v", err)
			}
			for _, ci := range user.ContactInfos {
				if ci.Type == models.ContactTypePhone {
					t.Error("the phone was not deleted")
				}
			}
			s.whatsAppQueue.Wait()
		})
		if got := q.calls(); len(got) != 3 {
			t.Errorf("expected 3 attempts, got %d", len(got))
		}
		if strings.Contains(logged, "WA_ALERT") {
			t.Errorf("an alert was raised although the retry got through: %q", logged)
		}
	})

	t.Run("down throughout: alert, the action still succeeds", func(t *testing.T) {
		s, q := cancelTestServer(t)
		q.failures = 100
		token := cancelTestUser(t, "wa_cancel_down@test.com", whatsAppAndEmail)
		phone, _ := phoneOf(t, token.Id)
		logged := captureAlerts(t, func() {
			if _, err := s.DeletePhoneNumber(context.Background(), token); err != nil {
				t.Fatalf("the participant's action failed: %v", err)
			}
			s.whatsAppQueue.Wait()
		})
		if got := q.calls(); len(got) != 3 {
			t.Errorf("expected 3 attempts, got %d", len(got))
		}
		if !strings.Contains(logged, "WA_ALERT cancel_failed user="+token.Id) {
			t.Errorf("missing alert: %q", logged)
		}
		if strings.Contains(logged, phone.Phone) || strings.Contains(logged, utils.MaskPhone(phone.Phone)) {
			t.Errorf("the alert carries the number: %q", logged)
		}
		if _, found := phoneOf(t, token.Id); found {
			t.Error("the phone is still there")
		}
	})

	t.Run("the participant's request does not wait for the retries", func(t *testing.T) {
		s, q := cancelTestServer(t)
		q.failures = 100
		s.whatsAppQueue.Backoff = []time.Duration{time.Second, time.Second}
		token := cancelTestUser(t, "wa_cancel_no_wait@test.com", whatsAppAndEmail)
		start := time.Now()
		captureAlerts(t, func() {
			if _, err := s.DeletePhoneNumber(context.Background(), token); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Errorf("the request waited %v for messaging", elapsed)
			}
			s.whatsAppQueue.Wait()
		})
	})
}

// In production the server builds its canceller over the messaging client it was given.
func TestNewUserManagementServerCancelsThroughTheMessagingClient(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	messaging := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
	logging := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)
	logging.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	token := cancelTestUser(t, "wa_cancel_wiring@test.com", whatsAppAndEmail)
	messaging.EXPECT().DeleteOutgoingWhatsAppForUser(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *messageAPI.DeleteOutgoingWhatsAppForUserReq, _ ...grpc.CallOption) (*messageAPI.DeleteOutgoingWhatsAppForUserResp, error) {
			if in.UserId != token.Id {
				t.Errorf("cancellation for %s, want %s", in.UserId, token.Id)
			}
			return &messageAPI.DeleteOutgoingWhatsAppForUserResp{}, nil
		}).Times(1)

	server := NewUserManagementServer(&models.APIClients{MessagingService: messaging, LoggingService: logging},
		testUserDBService, testGlobalDBService, models.Intervals{}, 100, utils.CreateWeekdayDefaultStrategy(), []string{testInstanceID},
		&countingWhatsAppClient{}, config.NewWhatsAppSettings(config.WhatsAppConfig{Enabled: true})).(*userManagementServer)
	if _, err := server.DeletePhoneNumber(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	server.whatsAppQueue.Wait()
}

func TestWithdrawnWhatsApp(t *testing.T) {
	both := []string{models.ChannelEmail, models.ChannelWhatsApp}
	cases := []struct {
		name          string
		before, after models.ContactPreferences
		types         []string
		withdrawn     bool
	}{
		{"whatsapp removed", models.ContactPreferences{PreferredChannels: both}, models.ContactPreferences{PreferredChannels: []string{models.ChannelEmail}}, nil, true},
		{"whatsapp kept", models.ContactPreferences{PreferredChannels: both}, models.ContactPreferences{PreferredChannels: both}, nil, false},
		{"legacy to none", models.ContactPreferences{}, models.ContactPreferences{PreferredChannels: []string{}}, nil, false},
		{"weekly off", models.ContactPreferences{SubscribedToWeekly: true}, models.ContactPreferences{}, []string{constants.EMAIL_TYPE_WEEKLY}, true},
		{"weekly on", models.ContactPreferences{}, models.ContactPreferences{SubscribedToWeekly: true}, nil, false},
		{"newsletter off", models.ContactPreferences{SubscribedToNewsletter: true}, models.ContactPreferences{}, []string{constants.EMAIL_TYPE_NEWSLETTER}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			types, withdrawn := withdrawnWhatsApp(c.before, c.after)
			if withdrawn != c.withdrawn || !reflect.DeepEqual(types, c.types) {
				t.Errorf("got %v %v, want %v %v", types, withdrawn, c.types, c.withdrawn)
			}
		})
	}
}
