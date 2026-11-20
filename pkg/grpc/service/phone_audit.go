package service

import (
	"fmt"
	"strings"

	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Audit events of the phone number, the notification channels and the subscriptions. The message of each
// event is "outcome=<outcome>", followed by the number masked, never in full.
const (
	LOG_EVENT_PHONE_ADD          = "PHONE ADD"
	LOG_EVENT_PHONE_CHANGE       = "PHONE CHANGE"
	LOG_EVENT_PHONE_CODE_RESEND  = "PHONE CODE RESEND"
	LOG_EVENT_PHONE_DELETE       = "PHONE DELETE"
	LOG_EVENT_PHONE_VERIFICATION = "PHONE VERIFICATION"
	LOG_EVENT_CHANNELS_CHANGED   = "NOTIFICATION CHANNELS CHANGED"
	// The participant opted out of WhatsApp through WhatsApp itself, or Meta reported the number
	// as unreachable, and the channel was removed without their acting in the settings.
	LOG_EVENT_WHATSAPP_CHANNEL_REMOVED = "WHATSAPP CHANNEL REMOVED"
	// The participant turned a subscription off with the unsubscribe link of a message, without
	// logging in. The message is "subscription=<name> outcome=<outcome>".
	LOG_EVENT_UNSUBSCRIBE_LINK = "UNSUBSCRIBE LINK"
)

// Outcomes set explicitly where the returned error does not tell them apart.
const (
	outcomeRateLimitedAccount     = "rate_limited_account"
	outcomeRateLimitedDestination = "rate_limited_destination"
)

// phoneAudit records the audit event of one phone request, whatever way the request ends: the
// endpoint creates it once the caller is known and records it in a deferred call with the error
// it returns, so that no return path can skip the event.
type phoneAudit struct {
	s          *userManagementServer
	instanceID string
	userID     string
	event      string
	// phone is the number the request is about, as typed until the endpoint has normalised it.
	phone string
	// previous is the number a change replaces.
	previous string
	// success is the outcome of a request that returns no error.
	success string
	// outcome, when set, is the outcome whatever the error.
	outcome string
}

func (s *userManagementServer) newPhoneAudit(token *api_types.TokenInfos, event, phone, success string) *phoneAudit {
	return &phoneAudit{s: s, instanceID: token.InstanceId, userID: token.Id, event: event, phone: phone, success: success}
}

// record saves the event for the error the endpoint returns.
func (a *phoneAudit) record(err error) {
	outcome, eventType := a.outcomeOf(err)
	msg := "outcome=" + outcome
	if a.phone != "" {
		msg += " phone=" + utils.MaskPhone(a.phone)
	}
	if a.previous != "" {
		msg += " previous=" + utils.MaskPhone(a.previous)
	}
	a.s.SaveLogEvent(a.instanceID, a.userID, eventType, a.event, msg)
}

// outcomeOf names what happened. Refusals carry the fixed message the endpoint answered with;
// internal errors are only "error", since their text can come from the database.
func (a *phoneAudit) outcomeOf(err error) (string, loggingAPI.LogEventType) {
	st := status.Convert(err)
	eventType := loggingAPI.LogEventType_LOG
	switch {
	case err == nil:
	case st.Code() == codes.ResourceExhausted, st.Code() == codes.PermissionDenied,
		st.Message() == "phone number already taken":
		eventType = loggingAPI.LogEventType_SECURITY
	case st.Code() == codes.Internal, st.Code() == codes.Unknown:
		eventType = loggingAPI.LogEventType_ERROR
	}
	switch {
	case a.outcome != "":
		return a.outcome, eventType
	case err == nil:
		return a.success, eventType
	case st.Code() == codes.Internal, st.Code() == codes.Unknown:
		return "error", eventType
	case st.Code() == codes.ResourceExhausted:
		return "rate_limited", eventType
	}
	return strings.ReplaceAll(st.Message(), " ", "_"), eventType
}

// recordChannelsChanged saves the audit event of a change of the notification channels, when the
// channels did change. Both lists are given as they are meant (a legacy account reads as e-mail).
func (s *userManagementServer) recordChannelsChanged(instanceID, userID string, before, after models.ContactPreferences) {
	from, to := before.EffectivePreferredChannels(), after.EffectivePreferredChannels()
	if strings.Join(from, ",") == strings.Join(to, ",") {
		return
	}
	s.SaveLogEvent(instanceID, userID, loggingAPI.LogEventType_LOG, LOG_EVENT_CHANNELS_CHANGED,
		fmt.Sprintf("from=%v to=%v", from, to))
}
