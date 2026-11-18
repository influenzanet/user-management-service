package service

import (
	"context"
	"fmt"

	"github.com/coneno/logger"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// whatsAppRemovalReasons names, for the audit event and the logs, the reasons a caller may give.
// A reason not listed here is refused.
var whatsAppRemovalReasons = map[api.WhatsAppRemovalReason]string{
	api.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_STOP_KEYWORD:  "stop_keyword",
	api.WhatsAppRemovalReason_WHATSAPP_REMOVAL_REASON_UNDELIVERABLE: "undeliverable",
}

// RemoveWhatsAppChannel takes whatsapp out of a participant's notification channels after an
// opt-out that arrived through WhatsApp itself: a stop reply, or Meta reporting the number as
// unreachable. The caller is another service; the participant is named by account or by the
// number the reply came from, which is resolved here to the account holding it verified.
//
// Only the channel goes: the phone number stays, and nothing is added in its place, so a
// participant reached on whatsapp only is left with no channel until they choose one in the
// settings. When the channel was removed, an audit event is written and the participant's queued
// WhatsApp messages are cancelled.
//
// Meta repeats its notifications, so the same opt-out may arrive more than once: a call that
// finds no account, or whatsapp already gone, changes nothing and answers channel_removed=false.
func (s *userManagementServer) RemoveWhatsAppChannel(ctx context.Context, req *api.RemoveWhatsAppChannelReq) (*api.RemoveWhatsAppChannelResp, error) {
	if req == nil || req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	reason, known := whatsAppRemovalReasons[req.Reason]
	if !known {
		return nil, status.Error(codes.InvalidArgument, "unknown reason")
	}

	var (
		userID primitive.ObjectID
		phone  string
	)
	switch target := req.Target.(type) {
	case *api.RemoveWhatsAppChannelReq_UserId:
		id, err := primitive.ObjectIDFromHex(target.UserId)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "user id not valid")
		}
		userID = id
	case *api.RemoveWhatsAppChannelReq_PhoneNumber:
		// Claims are keyed by the E.164 form, whatever the way the caller spells the number.
		normalized, err := utils.NormalizePhone(target.PhoneNumber)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "phone not valid")
		}
		holder, found, err := s.userDBservice.FindVerifiedPhoneHolder(req.InstanceId, normalized)
		if err != nil {
			logger.Error.Printf("RemoveWhatsAppChannel: %v", err)
			return nil, status.Error(codes.Internal, "could not look up the phone number")
		}
		if !found {
			logger.Info.Printf("RemoveWhatsAppChannel: reason=%s phone=%s outcome=no_account", reason, utils.MaskPhone(normalized))
			return &api.RemoveWhatsAppChannelResp{ChannelRemoved: false}, nil
		}
		userID, phone = holder, normalized
	default:
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	before, removed, err := s.userDBservice.RemoveWhatsAppChannel(req.InstanceId, userID, phone)
	if err != nil {
		logger.Error.Printf("RemoveWhatsAppChannel: user=%s: %v", userID.Hex(), err)
		return nil, status.Error(codes.Internal, "could not remove the channel")
	}
	if !removed {
		logger.Info.Printf("RemoveWhatsAppChannel: reason=%s user=%s outcome=not_enabled", reason, userID.Hex())
		return &api.RemoveWhatsAppChannelResp{ChannelRemoved: false}, nil
	}

	if phone == "" {
		phone = verifiedPhoneOf(before)
	}
	s.recordWhatsAppChannelRemoved(req.InstanceId, userID.Hex(), reason, phone, before.ContactPreferences)
	s.whatsAppQueue.CancelInBackground(req.InstanceId, userID.Hex(), nil, false)
	return &api.RemoveWhatsAppChannelResp{ChannelRemoved: true}, nil
}

// recordWhatsAppChannelRemoved saves the audit event of an opt-out, with the channels before and
// after it and the number masked.
func (s *userManagementServer) recordWhatsAppChannelRemoved(instanceID, userID, reason, phone string, before models.ContactPreferences) {
	from := before.EffectivePreferredChannels()
	to := []string{}
	for _, channel := range from {
		if channel != models.ChannelWhatsApp {
			to = append(to, channel)
		}
	}
	msg := "outcome=removed reason=" + reason
	if phone != "" {
		msg += " phone=" + utils.MaskPhone(phone)
	}
	msg += fmt.Sprintf(" from=%v to=%v", from, to)
	s.SaveLogEvent(instanceID, userID, loggingAPI.LogEventType_LOG, LOG_EVENT_WHATSAPP_CHANNEL_REMOVED, msg)
}

// verifiedPhoneOf returns the account's verified phone number, or "" when it has none.
func verifiedPhoneOf(user models.User) string {
	for _, ci := range user.ContactInfos {
		if ci.Type == models.ContactTypePhone && ci.ConfirmedAt > 0 {
			return ci.Phone
		}
	}
	return ""
}
