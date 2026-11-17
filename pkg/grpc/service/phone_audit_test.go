package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coneno/logger"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/api"
	httpClients "github.com/influenzanet/user-management-service/pkg/http/clients"
	"github.com/influenzanet/user-management-service/pkg/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc"
)

// Item 56: every outcome of the phone endpoints, and every change of the notification channels,
// leaves an audit event in the logging service, naming the number masked and never in full.

type auditRecorder struct {
	loggingAPI.LoggingServiceApiClient
	mu     sync.Mutex
	events []*loggingAPI.NewLogEvent
}

func (r *auditRecorder) SaveLogEvent(_ context.Context, in *loggingAPI.NewLogEvent, _ ...grpc.CallOption) (*api_types.ServiceStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, in)
	return &api_types.ServiceStatus{}, nil
}

// only returns the single event recorded since the last call, failing when there is not exactly
// one, and clears the record.
func (r *auditRecorder) only(t *testing.T) *loggingAPI.NewLogEvent {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	events := r.events
	r.events = nil
	if len(events) != 1 {
		t.Fatalf("expected exactly one audit event, got %d: %v", len(events), events)
	}
	return events[0]
}

func (r *auditRecorder) none(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != 0 {
		t.Errorf("expected no audit event, got %v", r.events)
	}
	r.events = nil
}

// requireAudit checks one event: name, type, message, and that no number in phones appears in
// full.
func requireAudit(t *testing.T, ev *loggingAPI.NewLogEvent, userID, name string, eventType loggingAPI.LogEventType, msg string, phones ...string) {
	t.Helper()
	if ev.EventName != name || ev.EventType != eventType || ev.Msg != msg || ev.UserId != userID || ev.InstanceId != testInstanceID {
		t.Errorf("unexpected audit event:\n got  %s %s %q user=%s\n want %s %s %q user=%s",
			ev.EventName, ev.EventType, ev.Msg, ev.UserId, name, eventType, msg, userID)
	}
	for _, phone := range phones {
		if strings.Contains(ev.Msg, phone) {
			t.Errorf("the event carries the full number %s: %q", phone, ev.Msg)
		}
	}
}

func auditTestServer(t *testing.T, whatsApp WhatsAppClient) (*userManagementServer, *auditRecorder) {
	t.Helper()
	s := newPhoneTestServer(whatsApp)
	rec := &auditRecorder{}
	s.clients = &models.APIClients{LoggingService: rec}
	// The cancellations of queued WhatsApp messages are not what these tests look at.
	withQueue(&s, &queueRecorder{})
	return &s, rec
}

var auditPhoneSeq int64

func auditPhone() string {
	auditPhoneSeq++
	return fmt.Sprintf("+3934%07d", 7000000+auditPhoneSeq)
}

// masked is utils.MaskPhone, spelled out so the test does not depend on the code it checks.
func masked(phone string) string { return phone[:3] + "***" + phone[len(phone)-4:] }

const (
	logEv = loggingAPI.LogEventType_LOG
	secEv = loggingAPI.LogEventType_SECURITY
	errEv = loggingAPI.LogEventType_ERROR
)

func TestAuditAddPhoneNumber(t *testing.T) {
	add := func(s *userManagementServer, token *api_types.TokenInfos, phone string) error {
		_, err := s.AddPhoneNumber(context.Background(), &api.PhoneMsg{Token: token, NewPhone: phone})
		return err
	}

	t.Run("code sent", func(t *testing.T) {
		s, rec := auditTestServer(t, &countingWhatsAppClient{})
		token := addRateLimitTestUser(t, "audit_add_ok@test.com", nil)
		phone := auditPhone()
		// Typed with spaces: the event names the number as stored.
		if err := add(s, &token, phone[:3]+" "+phone[3:]); err != nil {
			t.Fatal(err)
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_ADD, logEv, "outcome=code_sent phone="+masked(phone), phone)
	})

	t.Run("refusals", func(t *testing.T) {
		s, rec := auditTestServer(t, &countingWhatsAppClient{})
		token := addRateLimitTestUser(t, "audit_add_refused@test.com", nil)
		if err := add(s, &token, "not-a-number"); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_ADD, logEv, "outcome=phone_not_valid phone=not***mber")

		verified := addVerifyCodeTestUser(t, "audit_add_verified@test.com", auditPhone(), models.VerificationCode{}, nil, nil)
		own, _ := phoneOf(t, verified.Id)
		if _, err := testUserDBService.FinalizePhoneVerification(testInstanceID, verified.Id, own.Phone); err != nil {
			t.Fatal(err)
		}
		if err := add(s, &verified, own.Phone); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), verified.Id, LOG_EVENT_PHONE_ADD, logEv, "outcome=phone_number_already_verified phone="+masked(own.Phone), own.Phone)

		other := auditPhone()
		if err := add(s, &verified, other); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), verified.Id, LOG_EVENT_PHONE_ADD, logEv, "outcome=user_already_has_a_phone_number phone="+masked(other), other)
	})

	t.Run("rate limits", func(t *testing.T) {
		s, rec := auditTestServer(t, &countingWhatsAppClient{})
		token := addRateLimitTestUser(t, "audit_add_limited@test.com", recentAttempts(allowedPhoneVerificationAttempts))
		phone := auditPhone()
		if err := add(s, &token, phone); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_ADD, secEv, "outcome=rate_limited_account phone="+masked(phone), phone)

		target := auditPhone()
		for i := 0; i < allowedPhoneDestinationSends; i++ {
			if ok, err := testUserDBService.ReservePhoneDestinationSendSlot(testInstanceID, target, phoneDestinationSendLimits); err != nil || !ok {
				t.Fatalf("setup: %v %v", ok, err)
			}
		}
		fresh := addRateLimitTestUser(t, "audit_add_dest_limited@test.com", nil)
		if err := add(s, &fresh, target); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), fresh.Id, LOG_EVENT_PHONE_ADD, secEv, "outcome=rate_limited_destination phone="+masked(target), target)
	})

	t.Run("send failures", func(t *testing.T) {
		s, rec := auditTestServer(t, &countingWhatsAppClient{err: errors.New("meta is down")})
		token := addRateLimitTestUser(t, "audit_add_send_failed@test.com", nil)
		phone := auditPhone()
		if err := add(s, &token, phone); err == nil {
			t.Fatal("expected a failure")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_ADD, errEv, "outcome=send_failed phone="+masked(phone), phone)

		s, rec = auditTestServer(t, &countingWhatsAppClient{err: httpClients.ErrRecipientNotAllowed})
		token = addRateLimitTestUser(t, "audit_add_not_allowed@test.com", nil)
		phone = auditPhone()
		if err := add(s, &token, phone); err == nil {
			t.Fatal("expected a failure")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_ADD, logEv,
			"outcome=phone_number_not_enabled_to_receive_WhatsApp_messages phone="+masked(phone), phone)
	})
}

func TestAuditEditPhoneNumber(t *testing.T) {
	s, rec := auditTestServer(t, &countingWhatsAppClient{})
	edit := func(token *api_types.TokenInfos, phone string) error {
		_, err := s.EditPhoneNumber(context.Background(), &api.PhoneMsg{Token: token, NewPhone: phone})
		return err
	}

	t.Run("a new number names the previous one", func(t *testing.T) {
		old := auditPhone()
		token := addVerifyCodeTestUser(t, "audit_edit_change@test.com", old, models.VerificationCode{}, nil, nil)
		next := auditPhone()
		if err := edit(&token, next); err != nil {
			t.Fatal(err)
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_CHANGE, logEv,
			"outcome=code_sent phone="+masked(next)+" previous="+masked(old), old, next)
	})

	t.Run("the same pending number is a resend", func(t *testing.T) {
		phone := auditPhone()
		token := addVerifyCodeTestUser(t, "audit_edit_resend@test.com", phone, models.VerificationCode{}, nil, nil)
		if err := edit(&token, phone); err != nil {
			t.Fatal(err)
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_CHANGE, logEv, "outcome=code_resent phone="+masked(phone), phone)
	})

	t.Run("no number to change", func(t *testing.T) {
		token := addRateLimitTestUser(t, "audit_edit_none@test.com", nil)
		phone := auditPhone()
		if err := edit(&token, phone); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_CHANGE, logEv, "outcome=user_has_no_phone_number_to_edit phone="+masked(phone), phone)
	})

	t.Run("rate limited", func(t *testing.T) {
		phone := auditPhone()
		token := addVerifyCodeTestUser(t, "audit_edit_limited@test.com", phone, models.VerificationCode{}, nil, recentAttempts(allowedPhoneVerificationAttempts))
		if err := edit(&token, phone); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_CHANGE, secEv, "outcome=rate_limited_account phone="+masked(phone), phone)
	})
}

func TestAuditDeletePhoneNumber(t *testing.T) {
	s, rec := auditTestServer(t, &countingWhatsAppClient{})
	phone := auditPhone()
	token := addVerifyCodeTestUser(t, "audit_delete@test.com", phone, models.VerificationCode{}, nil, nil)
	if _, err := s.DeletePhoneNumber(context.Background(), &token); err != nil {
		t.Fatal(err)
	}
	requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_DELETE, logEv, "outcome=deleted phone="+masked(phone), phone)

	t.Run("a failed deletion", func(t *testing.T) {
		missing := api_types.TokenInfos{Id: primitive.NewObjectID().Hex(), InstanceId: testInstanceID}
		if _, err := s.DeletePhoneNumber(context.Background(), &missing); err == nil {
			t.Fatal("expected a failure")
		}
		requireAudit(t, rec.only(t), missing.Id, LOG_EVENT_PHONE_DELETE, errEv, "outcome=error")
	})
}

func TestAuditVerifyWhatsAppCode(t *testing.T) {
	s, rec := auditTestServer(t, &countingWhatsAppClient{})
	verify := func(token *api_types.TokenInfos, code string) error {
		_, err := s.VerifyWhatsAppCode(context.Background(), &api.VerifyWhatsAppCodeReq{Token: token, Code: code})
		return err
	}

	t.Run("verified", func(t *testing.T) {
		phone := auditPhone()
		token := addVerifyCodeTestUser(t, "audit_verify_ok@test.com", phone, validCode("123456"), nil, nil)
		if err := verify(&token, "123456"); err != nil {
			t.Fatal(err)
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_VERIFICATION, logEv, "outcome=verified phone="+masked(phone), phone)
	})

	t.Run("a wrong code", func(t *testing.T) {
		phone := auditPhone()
		token := addVerifyCodeTestUser(t, "audit_verify_wrong@test.com", phone, validCode("123456"), nil, nil)
		if err := verify(&token, "000000"); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_VERIFICATION, secEv, "outcome=invalid_verification_code phone="+masked(phone), phone)
	})

	t.Run("the removal after too many wrong codes", func(t *testing.T) {
		phone := auditPhone()
		exhausted := validCode("123456")
		exhausted.Attempts = 3
		token := addVerifyCodeTestUser(t, "audit_verify_exhausted@test.com", phone, exhausted, nil, nil)
		if err := verify(&token, "000000"); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_VERIFICATION, secEv, "outcome=removed_after_failed_attempts phone="+masked(phone), phone)
	})

	t.Run("an expired code", func(t *testing.T) {
		phone := auditPhone()
		expired := validCode("123456")
		expired.ExpiresAt = time.Now().Unix() - 1
		token := addVerifyCodeTestUser(t, "audit_verify_expired@test.com", phone, expired, nil, nil)
		if err := verify(&token, "123456"); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_VERIFICATION, secEv, "outcome=verification_code_expired")
	})

	t.Run("nothing to verify", func(t *testing.T) {
		token := addVerifyCodeTestUser(t, "audit_verify_none@test.com", auditPhone(), models.VerificationCode{}, nil, nil)
		if err := verify(&token, "123456"); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_VERIFICATION, logEv, "outcome=no_phone_verification_in_progress")
	})

	t.Run("a number another account holds", func(t *testing.T) {
		phone := auditPhone()
		seedPhoneOwner(t, "audit_verify_owner@test.com", phone)
		token := addVerifyCodeTestUser(t, "audit_verify_taken@test.com", phone, validCode("123456"), nil, nil)
		if err := verify(&token, "123456"); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_VERIFICATION, secEv, "outcome=phone_number_already_taken phone="+masked(phone), phone)
	})
}

func TestAuditResendPhoneCode(t *testing.T) {
	s, rec := auditTestServer(t, &countingWhatsAppClient{})
	resend := func(token *api_types.TokenInfos, address string) error {
		_, err := s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
			Token: token, Type: models.ContactTypePhone, Address: address,
		})
		return err
	}
	confirmedUser := func(t *testing.T, accountID, phone string, attempts []int64) *api_types.TokenInfos {
		t.Helper()
		users, err := addTestUsers([]models.User{{
			Account: models.Account{Type: "email", AccountID: accountID, AccountConfirmedAt: time.Now().Unix(), PhoneVerificationAttempts: attempts},
			ContactInfos: []models.ContactInfo{
				{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: accountID, ConfirmedAt: time.Now().Unix()},
				{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone},
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return &api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}
	}

	t.Run("code sent", func(t *testing.T) {
		phone := auditPhone()
		token := confirmedUser(t, "audit_resend_ok@test.com", phone, nil)
		if err := resend(token, phone); err != nil {
			t.Fatal(err)
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_CODE_RESEND, logEv, "outcome=code_sent phone="+masked(phone), phone)
	})

	t.Run("rate limited", func(t *testing.T) {
		phone := auditPhone()
		token := confirmedUser(t, "audit_resend_limited@test.com", phone, recentAttempts(allowedPhoneVerificationAttempts))
		if err := resend(token, phone); err == nil {
			t.Fatal("expected a refusal")
		}
		requireAudit(t, rec.only(t), token.Id, LOG_EVENT_PHONE_CODE_RESEND, secEv, "outcome=rate_limited_account phone="+masked(phone), phone)
	})

	t.Run("an e-mail resend is not a phone event", func(t *testing.T) {
		// The e-mail branch has no audit event of its own; it must not get the phone one.
		token := confirmedUser(t, "audit_resend_email@test.com", auditPhone(), nil)
		_, _ = s.ResendContactVerification(context.Background(), &api.ResendContactVerificationReq{
			Token: token, Type: models.ContactTypeEmail, Address: "nobody@test.com",
		})
		rec.none(t)
	})
}

func TestAuditChannelsChanged(t *testing.T) {
	s, rec := auditTestServer(t, &countingWhatsAppClient{})
	update := func(token *api_types.TokenInfos, channels []string) {
		t.Helper()
		if _, err := s.UpdateContactPreferences(context.Background(), &api.ContactPreferencesMsg{
			Token: token, ContactPreferences: &api.ContactPreferences{PreferredChannels: channels},
		}); err != nil {
			t.Fatal(err)
		}
	}

	token := cancelTestUser(t, "audit_channels@test.com", models.ContactPreferences{PreferredChannels: []string{models.ChannelEmail, models.ChannelWhatsApp}})
	update(token, []string{models.ChannelWhatsApp})
	requireAudit(t, rec.only(t), token.Id, LOG_EVENT_CHANNELS_CHANGED, logEv, "from=[email whatsapp] to=[whatsapp]")

	update(token, []string{})
	requireAudit(t, rec.only(t), token.Id, LOG_EVENT_CHANNELS_CHANGED, logEv, "from=[whatsapp] to=[]")

	t.Run("no change, no event", func(t *testing.T) {
		update(token, []string{})
		rec.none(t)
	})

	t.Run("a legacy account reads as e-mail", func(t *testing.T) {
		legacy := cancelTestUser(t, "audit_channels_legacy@test.com", models.ContactPreferences{})
		update(legacy, []string{models.ChannelEmail})
		rec.none(t)
		update(legacy, []string{models.ChannelWhatsApp})
		requireAudit(t, rec.only(t), legacy.Id, LOG_EVENT_CHANNELS_CHANGED, logEv, "from=[email] to=[whatsapp]")
	})
}

// A server without a logging client logs the failure instead of panicking.
func TestSaveLogEventWithoutALoggingClient(t *testing.T) {
	var buf bytes.Buffer
	original := logger.Error.Writer()
	logger.Error.SetOutput(&buf)
	defer logger.Error.SetOutput(original)

	(&userManagementServer{}).SaveLogEvent(testInstanceID, "u", logEv, LOG_EVENT_PHONE_ADD, "outcome=code_sent")
	(&userManagementServer{clients: &models.APIClients{}}).SaveLogEvent(testInstanceID, "u", logEv, LOG_EVENT_PHONE_ADD, "outcome=code_sent")
	if strings.Count(buf.String(), "failed to save log PHONE ADD: no logging service client") != 2 {
		t.Errorf("unexpected log: %q", buf.String())
	}
}
