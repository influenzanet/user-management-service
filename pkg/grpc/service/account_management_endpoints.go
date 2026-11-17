package service

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/coneno/logger"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	httpClients "github.com/influenzanet/user-management-service/pkg/http/clients"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/pwhash"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *userManagementServer) GetUser(ctx context.Context, req *api.UserReference) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if req.UserId == "" {
		req.UserId = req.Token.Id
	}

	if req.Token.Id != req.UserId { // Later can be overwritten
		logger.Warning.Printf("SECURITY WARNING: not authorized GetUser(): %s tried to access %s", req.Token.Id, req.UserId)
		return nil, status.Error(codes.PermissionDenied, "not authorized")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.UserId)
	if err != nil {
		return nil, status.Error(codes.Internal, "not found")
	}
	return user.ToAPI(), nil
}

func (s *userManagementServer) ChangePassword(ctx context.Context, req *api.PasswordChangeMsg) (*api.ServiceStatus, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if !utils.CheckPasswordFormat(req.NewPassword) {
		return nil, status.Error(codes.InvalidArgument, "new password too weak")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user and/or password")
	}

	match, err := pwhash.ComparePasswordWithHash(user.Account.Password, req.OldPassword)
	if err != nil || !match {
		s.SaveLogEvent(req.Token.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_AUTH_WRONG_PASSWORD, "change password endpoint")
		return nil, status.Error(codes.InvalidArgument, "invalid user and/or password")
	}

	newHashedPw, err := pwhash.HashPassword(req.NewPassword)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	err = s.userDBservice.UpdateUserPassword(req.Token.InstanceId, req.Token.Id, newHashedPw)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	logger.Info.Printf("user %s initiated password change", req.Token.Id)

	// Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:        req.Token.InstanceId,
		To:                []string{user.Account.AccountID},
		MessageType:       constants.EMAIL_TYPE_PASSWORD_CHANGED,
		PreferredLanguage: user.Account.PreferredLanguage,
		UseLowPrio:        true,
	})
	if err != nil {
		logger.Error.Printf("ChangePassword: %s", err.Error())
	}
	// ---

	// remove all temptokens for password reset:
	if err := s.globalDBService.DeleteAllTempTokenForUser(req.Token.InstanceId, req.Token.Id, constants.TOKEN_PURPOSE_PASSWORD_RESET); err != nil {
		logger.Error.Printf("ChangePassword: %s", err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_PASSWORD_CHANGED, "")

	return &api.ServiceStatus{
		Status: api.ServiceStatus_NORMAL,
		Msg:    "password changed",
	}, nil
}

func (s *userManagementServer) ChangeAccountIDEmail(ctx context.Context, req *api.EmailChangeMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.NewEmail == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	req.NewEmail = utils.SanitizeEmail(req.NewEmail)
	if !utils.CheckEmailFormat(req.NewEmail) {
		return nil, status.Error(codes.InvalidArgument, "email not valid")
	}
	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	match, err := pwhash.ComparePasswordWithHash(user.Account.Password, req.Password)
	if err != nil || !match {
		s.SaveLogEvent(req.Token.InstanceId, user.ID.Hex(), loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_AUTH_WRONG_PASSWORD, "change account id endpoint")
		return nil, status.Error(codes.InvalidArgument, "action failed")
	}

	// is email address still free to use?
	_, err = s.userDBservice.GetUserByAccountID(req.Token.InstanceId, req.NewEmail)
	if err == nil {
		return nil, status.Error(codes.Internal, "action failed")
	}

	if user.Account.Type != models.ACCOUNT_TYPE_EMAIL {
		return nil, status.Error(codes.Internal, "account is not email type")
	}
	oldCI, oldFound := user.FindContactInfoByTypeAndAddr("email", user.Account.AccountID)
	if !oldFound {
		return nil, status.Error(codes.Internal, "old contact info not found - unexpected error")
	}

	if user.Account.AccountConfirmedAt > 0 {
		// Old AccountID already confirmed

		// TempToken for contact verification:
		tempTokenInfos := models.TempToken{
			UserID:     user.ID.Hex(),
			InstanceID: req.Token.InstanceId,
			Purpose:    constants.TOKEN_PURPOSE_RESTORE_ACCOUNT_ID,
			Info: map[string]string{
				"oldEmail": user.Account.AccountID,
				"newEmail": req.NewEmail,
			},
			Expiration: tokens.GetExpirationTime(time.Hour * 24 * 7),
		}
		tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		// ---> Trigger message sending
		_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
			InstanceId:        req.Token.InstanceId,
			To:                []string{user.Account.AccountID},
			MessageType:       constants.EMAIL_TYPE_ACCOUNT_ID_CHANGED,
			PreferredLanguage: user.Account.PreferredLanguage,
			ContentInfos: map[string]string{
				"restoreToken": tempToken,
				"validUntil":   strconv.Itoa(24 * 7 * 60),
				"newEmail":     req.NewEmail,
			},
			UseLowPrio: true,
		})
		if err != nil {
			logger.Error.Printf("ChangeAccountIDEmail: %s", err.Error())
		}
		// <---
	}
	// if old AccountID was not confirmed probably wrong address used in the first place
	if user.Profiles[0].Alias == user.Account.AccountID {
		user.Profiles[0].Alias = req.NewEmail
	}
	user.Account.AccountID = req.NewEmail
	user.Account.AccountConfirmedAt = -1

	// Add new address to contact list if necessary:
	ci, found := user.FindContactInfoByTypeAndAddr("email", req.NewEmail)
	if found {
		// new email already confirmed
		if ci.ConfirmedAt > 0 {
			user.Account.AccountConfirmedAt = ci.ConfirmedAt
		}
	} else {
		user.AddNewEmail(req.NewEmail, false)
	}

	newCI, newFound := user.FindContactInfoByTypeAndAddr("email", req.NewEmail)
	if !newFound {
		return nil, status.Error(codes.Internal, "new contact info not found - unexpected error")
	}
	user.ReplaceContactInfoInContactPreferences(oldCI.ID.Hex(), newCI.ID.Hex())

	// start confirmation workflow of necessary:
	if user.Account.AccountConfirmedAt <= 0 {
		// TempToken for contact verification:
		tempTokenInfos := models.TempToken{
			UserID:     user.ID.Hex(),
			InstanceID: req.Token.InstanceId,
			Purpose:    constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
			Info: map[string]string{
				"type":  "email",
				"email": user.Account.AccountID,
			},
			Expiration: tokens.GetExpirationTime(time.Hour * 24 * 30),
		}
		tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}

		// ---> Trigger message sending
		_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
			InstanceId:        req.Token.InstanceId,
			To:                []string{user.Account.AccountID},
			MessageType:       constants.EMAIL_TYPE_VERIFY_EMAIL,
			PreferredLanguage: user.Account.PreferredLanguage,
			ContentInfos: map[string]string{
				"token": tempToken,
			},
		})
		if err != nil {
			logger.Error.Printf("ChangeAccountIDEmail: %s", err.Error())
		}
		// <---
	}

	if !req.KeepOldEmail {
		err := user.RemoveContactInfo(oldCI.ID.Hex())
		if err != nil {
			logger.Error.Println(err.Error())
		}
	}

	// Save user:
	updUser, err := s.userDBservice.UpdateUser(req.Token.InstanceId, user,
		"account.accountID", "account.accountConfirmedAt", "profiles", "contactInfos", "contactPreferences.sendNewsletterTo")
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, updUser.ID.Hex(), loggingAPI.LogEventType_LOG, constants.LOG_EVENT_ACCOUNT_ID_CHANGED, updUser.Account.AccountID)

	return updUser.ToAPI(), nil
}

func (s *userManagementServer) DeleteAccount(ctx context.Context, req *api.UserReference) (*api.ServiceStatus, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	// TODO: check if user auth is from admin - to remove user by admin
	if req.Token.Id != req.UserId {
		logger.Warning.Printf("unauthorized request: user %s initiated account removal for user id %s", req.Token.Id, req.UserId)
		return nil, status.Error(codes.PermissionDenied, "not authorized")
	}
	logger.Info.Printf("user %s initiated account removal for user id %s", req.Token.Id, req.UserId)

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.UserId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// ---> Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:        req.Token.InstanceId,
		To:                []string{user.Account.AccountID},
		MessageType:       constants.EMAIL_TYPE_ACCOUNT_DELETED,
		PreferredLanguage: user.Account.PreferredLanguage,
		UseLowPrio:        true,
	})
	if err != nil {
		logger.Error.Printf("DeleteAccount: %s", err.Error())
	}
	// <---

	if err := s.userDBservice.DeleteUser(req.Token.InstanceId, req.UserId); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	// A deleted participant leaves no WhatsApp behind: nothing queued, nothing archived.
	s.whatsAppQueue.CancelInBackground(req.Token.InstanceId, req.UserId, nil, true)

	// remove all TempTokens for the given user ID using auth-service
	if err := s.globalDBService.DeleteAllTempTokenForUser(req.Token.InstanceId, req.Token.Id, ""); err != nil {
		logger.Error.Printf("error, when trying to remove temp-tokens: %s", err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_ACCOUNT_DELETED, user.Account.AccountID)

	logger.Info.Printf("user account with id %s successfully removed", req.UserId)
	return &api.ServiceStatus{
		Status: api.ServiceStatus_NORMAL,
		Msg:    "user deleted",
	}, nil
}

func (s *userManagementServer) ChangePreferredLanguage(ctx context.Context, req *api.LanguageChangeMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.LanguageCode == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	user, err := s.userDBservice.UpdateAccountPreferredLang(req.Token.InstanceId, req.Token.Id, req.LanguageCode)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return user.ToAPI(), nil
}

func (s *userManagementServer) SaveProfile(ctx context.Context, req *api.ProfileRequest) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.Profile == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	if req.Profile.Id == "" {
		if len(user.Profiles) > maximumProfilesAllowed {
			s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_SECURITY, constants.LOG_EVENT_PROFILE_SAVED, "too many profiles added"+req.Profile.Alias)
			return nil, status.Error(codes.Internal, "reached profile limit")
		}
		user.AddProfile(models.ProfileFromAPI(req.Profile))
	} else {
		err := user.UpdateProfile(models.ProfileFromAPI(req.Profile))
		if err != nil {
			return nil, status.Error(codes.Internal, "profile not found")
		}
	}

	updUser, err := s.userDBservice.UpdateUser(req.Token.InstanceId, user, "profiles")
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_PROFILE_SAVED, req.Profile.Alias)

	return updUser.ToAPI(), nil
}

func (s *userManagementServer) RemoveProfile(ctx context.Context, req *api.ProfileRequest) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.Profile == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	if len(user.Profiles) == 1 {
		return nil, status.Error(codes.Internal, "can't delete last profile")
	}

	if err := user.RemoveProfile(req.Profile.Id); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	updUser, err := s.userDBservice.UpdateUser(req.Token.InstanceId, user, "profiles")
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_PROFILE_REMOVED, "id: "+req.Profile.Id)
	return updUser.ToAPI(), nil
}

func (s *userManagementServer) UpdateContactPreferences(ctx context.Context, req *api.ContactPreferencesMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.ContactPreferences == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	prefs := models.ContactPreferencesFromAPI(req.ContactPreferences)
	channels, err := resolvePreferredChannels(prefs.PreferredChannels, user)
	if err != nil {
		return nil, err
	}
	prefs.PreferredChannels = channels

	updatedUser, err := s.userDBservice.UpdateContactPreferences(req.Token.InstanceId, req.Token.Id, prefs)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, status.Error(codes.InvalidArgument, "user or verified phone changed; reload preferences")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.recordChannelsChanged(req.Token.InstanceId, req.Token.Id, user.ContactPreferences, updatedUser.ContactPreferences)
	if messageTypes, withdrawn := withdrawnWhatsApp(user.ContactPreferences, updatedUser.ContactPreferences); withdrawn {
		s.whatsAppQueue.CancelInBackground(req.Token.InstanceId, req.Token.Id, messageTypes, false)
	}
	return updatedUser.ToAPI(), nil
}

// withdrawnWhatsApp tells which of the participant's queued WhatsApp messages a preferences change
// withdraws: all of them (no types) when whatsapp leaves the channels; otherwise the message
// types of the subscriptions turned off. withdrawn is false when the change withdraws nothing.
func withdrawnWhatsApp(before, after models.ContactPreferences) (messageTypes []string, withdrawn bool) {
	if before.HasPreferredChannel(models.ChannelWhatsApp) && !after.HasPreferredChannel(models.ChannelWhatsApp) {
		return nil, true
	}
	if before.SubscribedToWeekly && !after.SubscribedToWeekly {
		messageTypes = append(messageTypes, constants.EMAIL_TYPE_WEEKLY)
	}
	if before.SubscribedToNewsletter && !after.SubscribedToNewsletter {
		messageTypes = append(messageTypes, constants.EMAIL_TYPE_NEWSLETTER)
	}
	return messageTypes, len(messageTypes) > 0
}

// resolvePreferredChannels checks the requested notification channels against what the user can
// actually be reached on, and returns the list to persist: exactly the channels the user chose,
// deduplicated and in the order requested.
//
// An empty list is accepted and means no channel: the participant gets no notification on any
// channel. It is stored as such, never completed with e-mail. Transactional e-mails
// (password reset, verification, account) do not depend on it.
//
// The verified-phone requirement is enforced here and not only in the interface: the interface can
// only stop the honest client, while this is the boundary where the data enters the system.
func resolvePreferredChannels(requested []string, user models.User) ([]string, error) {
	hasVerifiedPhone := false
	for _, ci := range user.ContactInfos {
		if ci.Type == models.ContactTypePhone && ci.ConfirmedAt > 0 {
			hasVerifiedPhone = true
			break
		}
	}

	channels := make([]string, 0, len(requested))
	seen := map[string]bool{}
	for _, channel := range requested {
		switch channel {
		case models.ChannelEmail:
		case models.ChannelWhatsApp:
			if !hasVerifiedPhone {
				return nil, status.Error(codes.InvalidArgument, "whatsapp channel requires a verified phone number")
			}
		default:
			return nil, status.Errorf(codes.InvalidArgument, "unknown notification channel: %s", channel)
		}
		if !seen[channel] {
			seen[channel] = true
			channels = append(channels, channel)
		}
	}
	return channels, nil
}

func (s *userManagementServer) UseUnsubscribeToken(ctx context.Context, req *api.TempToken) (*api.ServiceStatus, error) {
	if req == nil || req.Token == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	tokenInfos, err := s.ValidateTempToken(req.Token, []string{constants.TOKEN_PURPOSE_UNSUBSCRIBE_NEWSLETTER})
	if err != nil {
		logger.Error.Printf("UseUnsubscribeToken: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	user, err := s.userDBservice.GetUserByID(tokenInfos.InstanceID, tokenInfos.UserID)
	if err != nil {
		logger.Error.Printf("UseUnsubscribeToken: %s", err.Error())
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	wasSubscribed := user.ContactPreferences.SubscribedToNewsletter
	user.ContactPreferences.SubscribedToNewsletter = false

	_, err = s.userDBservice.UpdateUser(tokenInfos.InstanceID, user, "contactPreferences.subscribedToNewsletter")
	if err != nil {
		logger.Error.Printf("UseUnsubscribeToken: %s", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}
	if wasSubscribed {
		s.whatsAppQueue.CancelInBackground(tokenInfos.InstanceID, tokenInfos.UserID, []string{constants.EMAIL_TYPE_NEWSLETTER}, false)
	}
	return &api.ServiceStatus{
		Status: api.ServiceStatus_NORMAL,
		Msg:    "unsubscribed",
	}, nil
}

func (s *userManagementServer) AddEmail(ctx context.Context, req *api.ContactInfoMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.ContactInfo == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	if req.ContactInfo.Type != "email" {
		return nil, status.Error(codes.InvalidArgument, "wrong contact type")
	}

	email := utils.SanitizeEmail(req.ContactInfo.GetEmail())
	if !utils.CheckEmailFormat(email) {
		return nil, status.Error(codes.InvalidArgument, "email not valid")
	}

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	user.AddNewEmail(email, false)

	// TempToken for contact verification:
	tempTokenInfos := models.TempToken{
		UserID:     user.ID.Hex(),
		InstanceID: req.Token.InstanceId,
		Purpose:    constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
		Info: map[string]string{
			"type":  "email",
			"email": email,
		},

		Expiration: tokens.GetExpirationTime(time.Hour * 24 * 30),
	}
	tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// ---> Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:  req.Token.InstanceId,
		To:          []string{user.Account.AccountID},
		MessageType: constants.EMAIL_TYPE_VERIFY_EMAIL,
		ContentInfos: map[string]string{
			"token": tempToken,
		},
		PreferredLanguage: user.Account.PreferredLanguage,
	})
	if err != nil {
		logger.Error.Printf("AddEmail: %s", err.Error())
	}
	// <---

	updUser, err := s.userDBservice.AddEmailContactInfo(req.Token.InstanceId, user.ID, user.ContactInfos[len(user.ContactInfos)-1])
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return updUser.ToAPI(), nil
}

func (s *userManagementServer) RemoveEmail(ctx context.Context, req *api.ContactInfoMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.ContactInfo == nil {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	err = user.RemoveContactInfo(req.ContactInfo.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	updUser, err := s.userDBservice.RemoveContactInfo(req.Token.InstanceId, user.ID, req.ContactInfo.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return updUser.ToAPI(), nil
}

func (s *userManagementServer) AddPhoneNumber(ctx context.Context, req *api.PhoneMsg) (_ *api.User, err error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.NewPhone == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	audit := s.newPhoneAudit(req.Token, LOG_EVENT_PHONE_ADD, req.NewPhone, "code_sent")
	defer func() { audit.record(err) }()
	if !s.whatsApp.Enabled(req.Token.InstanceId) {
		return nil, status.Error(codes.Unavailable, "WhatsApp is not configured")
	}

	// Numbers are stored and compared in E.164 form, whatever the way they were typed.
	phone, err := utils.NormalizePhone(req.NewPhone)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "phone not valid")
	}

	audit.phone = phone
	logger.Debug.Printf("AddPhoneNumber: phone=%s", utils.MaskPhone(phone))

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	// Check if user already has this phone number
	var existingPhoneInfo *models.ContactInfo
	for i, ci := range user.ContactInfos {
		if ci.Type == models.ContactTypePhone && utils.SamePhone(ci.Phone, phone) {
			existingPhoneInfo = &user.ContactInfos[i]
			// Continue with the number as stored, which differs from the normalised one only
			// for data written before numbers were normalised.
			phone = ci.Phone
			audit.phone = phone
			break
		}
	}

	// If user already has this phone and it's verified, can't add again
	if existingPhoneInfo != nil && existingPhoneInfo.ConfirmedAt > 0 {
		return nil, status.Error(codes.InvalidArgument, "phone number already verified")
	}

	isNewPhone := existingPhoneInfo == nil
	if isNewPhone {
		// Check if user already has a different phone number
		for _, ci := range user.ContactInfos {
			if ci.Type == models.ContactTypePhone && ci.Phone != "" && !utils.SamePhone(ci.Phone, phone) {
				return nil, status.Error(codes.InvalidArgument, "user already has a phone number")
			}
		}
	}

	// Atomically reserve a send slot before touching Meta: check and increment are one
	// find-and-modify, so overlapping requests (also across replicas) cannot exceed the
	// budget. The user was loaded above, so a failed reservation means the budget is gone.
	// A reserved slot stays consumed even if the send fails: a failed send still burned a
	// Meta call.
	// Whether another account carries or has proved the number is not asked here: the
	// answer is about a third party, and it would let an account holder walk a list of numbers
	// and learn which of them belong to participants. The code is sent either way; verification
	// refuses a number another account has proved (FinalizePhoneVerification).
	slotFree, err := s.userDBservice.ReservePhoneVerificationSlot(req.Token.InstanceId, req.Token.Id, allowedPhoneVerificationAttempts, phoneVerificationRateLimitWindow)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !slotFree {
		audit.outcome = outcomeRateLimitedAccount
		return nil, status.Error(codes.ResourceExhausted, "too many phone verification attempts, try again later")
	}

	// The account's budget bounds what one account spends, not what one number receives, and
	// accounts are free to create; the number's own budget, counted across every account, is
	// reserved here — past every refusal that sends nothing, before anything is written or sent
	// (see ReservePhoneDestinationSendSlot).
	destinationFree, err := s.userDBservice.ReservePhoneDestinationSendSlot(req.Token.InstanceId, phone, phoneDestinationSendLimits)
	if err != nil {
		logger.Error.Printf("AddPhoneNumber: %s", err.Error())
		return nil, status.Error(codes.Internal, "could not reserve the send budget")
	}
	if !destinationFree {
		audit.outcome = outcomeRateLimitedDestination
		return nil, status.Error(codes.ResourceExhausted, "too many phone verification attempts, try again later")
	}

	if isNewPhone {
		// The guard lives in the filter: only one concurrent request can add a phone.
		added, err := s.userDBservice.AddPhoneContactInfoIfAbsent(req.Token.InstanceId, req.Token.Id, models.ContactInfo{
			ID:    primitive.NewObjectID(),
			Type:  models.ContactTypePhone,
			Phone: phone,
		})
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		if !added {
			// A concurrent request added a phone first: only the same-number resend is allowed.
			fresh, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
			if err != nil {
				return nil, status.Error(codes.Internal, "user not found")
			}
			ci, found := fresh.FindContactInfoByTypeAndAddr(models.ContactTypePhone, phone)
			if !found {
				return nil, status.Error(codes.InvalidArgument, "user already has a phone number")
			}
			if ci.ConfirmedAt > 0 {
				return nil, status.Error(codes.InvalidArgument, "phone number already verified")
			}
		}
	}

	// The phone code is kept apart from the login 2FA code, and the targeted $set leaves the
	// atomically managed attempts array untouched.
	vc, code, err := s.newPhoneVerificationCode("AddPhoneNumber", req.Token.Id, phone)
	if err != nil {
		return nil, err
	}
	// Persist the code before sending, so a delivered code is always verifiable, and only
	// while this number is still the one waiting to be verified: a request that read the
	// account before the number changed is refused here, before anything reaches Meta.
	if err := s.userDBservice.SetPhoneVerificationCode(req.Token.InstanceId, req.Token.Id, code); err != nil {
		if errors.Is(err, userdb.ErrPhoneNotPending) {
			return nil, status.Error(codes.InvalidArgument, "phone number is not pending verification")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Send WhatsApp verification code
	if err := s.whatsAppClient.SendVerificationCode(ctx, phone, vc, verificationTemplateLang(s.whatsApp.ForInstance(req.Token.InstanceId), user)); err != nil {
		logger.Error.Printf("AddPhoneNumber: %s", err.Error())
		if errors.Is(err, httpClients.ErrRecipientNotAllowed) {
			return nil, status.Error(codes.FailedPrecondition, "phone number not enabled to receive WhatsApp messages")
		}
		audit.outcome = "send_failed"
		return nil, status.Error(codes.Internal, "failed to send verification code")
	}
	// Mark the cooldown only after the message was accepted
	if err := s.userDBservice.SetContactVerificationSentAt(req.Token.InstanceId, req.Token.Id, models.ContactTypePhone, phone, time.Now().Unix()); err != nil {
		logger.Error.Printf("AddPhoneNumber: %s", err.Error())
	}
	updUser, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		logger.Error.Printf("AddPhoneNumber: %s", err.Error())
		return user.ToAPI(), nil
	}
	return updUser.ToAPI(), nil
}

func (s *userManagementServer) EditPhoneNumber(ctx context.Context, req *api.PhoneMsg) (_ *api.User, err error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.NewPhone == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	audit := s.newPhoneAudit(req.Token, LOG_EVENT_PHONE_CHANGE, req.NewPhone, "code_sent")
	defer func() { audit.record(err) }()
	if !s.whatsApp.Enabled(req.Token.InstanceId) {
		return nil, status.Error(codes.Unavailable, "WhatsApp is not configured")
	}

	// Numbers are stored and compared in E.164 form, whatever the way they were typed.
	phone, err := utils.NormalizePhone(req.NewPhone)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "phone not valid")
	}

	audit.phone = phone
	logger.Debug.Printf("EditPhoneNumber: phone=%s", utils.MaskPhone(phone))

	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	var contactInfo *models.ContactInfo = nil

	// Check if user has a registered phone number
	for i := range user.ContactInfos {
		if user.ContactInfos[i].Type == models.ContactTypePhone {
			contactInfo = &user.ContactInfos[i]
			break
		}
	}

	if contactInfo == nil {
		return nil, status.Error(codes.InvalidArgument, "user has no phone number to edit")
	}

	changingNumber := false
	// If trying to set the same phone number that's already unverified, allow re-sending code
	if utils.SamePhone(contactInfo.Phone, phone) && contactInfo.ConfirmedAt == 0 {
		// Same phone, not verified — keep ContactInfo, just regenerate code
		phone = contactInfo.Phone
		audit.phone = phone
		audit.success = "code_resent"
	} else if utils.SamePhone(contactInfo.Phone, phone) && contactInfo.ConfirmedAt > 0 {
		return nil, status.Error(codes.InvalidArgument, "phone number already verified")
	} else {
		changingNumber = true
		audit.previous = contactInfo.Phone
	}

	// Atomically reserve a send slot before touching Meta (see AddPhoneNumber); the user was
	// loaded above, so a failed reservation means the budget is gone. A reserved slot stays
	// consumed even if the send fails: a failed send still burned a Meta call. As in
	// AddPhoneNumber, nothing here says whether another account holds the number.
	slotFree, err := s.userDBservice.ReservePhoneVerificationSlot(req.Token.InstanceId, req.Token.Id, allowedPhoneVerificationAttempts, phoneVerificationRateLimitWindow)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !slotFree {
		audit.outcome = outcomeRateLimitedAccount
		return nil, status.Error(codes.ResourceExhausted, "too many phone verification attempts, try again later")
	}

	// The account's budget bounds what one account spends, not what one number receives, and
	// accounts are free to create; the number's own budget, counted across every account, is
	// reserved here — past every refusal that sends nothing, before anything is written or sent
	// (see ReservePhoneDestinationSendSlot).
	destinationFree, err := s.userDBservice.ReservePhoneDestinationSendSlot(req.Token.InstanceId, phone, phoneDestinationSendLimits)
	if err != nil {
		logger.Error.Printf("EditPhoneNumber: %s", err.Error())
		return nil, status.Error(codes.Internal, "could not reserve the send budget")
	}
	if !destinationFree {
		audit.outcome = outcomeRateLimitedDestination
		return nil, status.Error(codes.ResourceExhausted, "too many phone verification attempts, try again later")
	}

	if changingNumber {
		// Overwrite the phone entry in place with a targeted update; no full-document replace.
		if err := s.userDBservice.ReplacePhoneContactInfo(req.Token.InstanceId, req.Token.Id, models.ContactInfo{
			ID:    primitive.NewObjectID(),
			Type:  models.ContactTypePhone,
			Phone: phone,
		}); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		// Messages queued for the old number go with it.
		s.whatsAppQueue.CancelInBackground(req.Token.InstanceId, req.Token.Id, nil, false)
	}

	vc, code, err := s.newPhoneVerificationCode("EditPhoneNumber", req.Token.Id, phone)
	if err != nil {
		return nil, err
	}
	// Persist the code before sending, so a delivered code is always verifiable, and only
	// while this number is still the one waiting to be verified: a request that read the
	// account before the number changed is refused here, before anything reaches Meta.
	if err := s.userDBservice.SetPhoneVerificationCode(req.Token.InstanceId, req.Token.Id, code); err != nil {
		if errors.Is(err, userdb.ErrPhoneNotPending) {
			return nil, status.Error(codes.InvalidArgument, "phone number is not pending verification")
		}
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Send WhatsApp verification code
	if err := s.whatsAppClient.SendVerificationCode(ctx, phone, vc, verificationTemplateLang(s.whatsApp.ForInstance(req.Token.InstanceId), user)); err != nil {
		logger.Error.Printf("EditPhoneNumber: %s", err.Error())
		if errors.Is(err, httpClients.ErrRecipientNotAllowed) {
			return nil, status.Error(codes.FailedPrecondition, "phone number not enabled to receive WhatsApp messages")
		}
		audit.outcome = "send_failed"
		return nil, status.Error(codes.Internal, "failed to send verification code")
	}
	// Mark the cooldown only after the message was accepted
	if err := s.userDBservice.SetContactVerificationSentAt(req.Token.InstanceId, req.Token.Id, models.ContactTypePhone, phone, time.Now().Unix()); err != nil {
		logger.Error.Printf("EditPhoneNumber: %s", err.Error())
	}
	updUser, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		logger.Error.Printf("EditPhoneNumber: %s", err.Error())
		return user.ToAPI(), nil
	}
	return updUser.ToAPI(), nil
}

func (s *userManagementServer) DeletePhoneNumber(ctx context.Context, req *api_types.TokenInfos) (_ *api.User, err error) {

	if req == nil || utils.IsTokenEmpty(req) {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	audit := s.newPhoneAudit(req, LOG_EVENT_PHONE_DELETE, "", "deleted")
	defer func() { audit.record(err) }()
	// Read only to name the number in the audit event.
	if current, readErr := s.userDBservice.GetUserByID(req.InstanceId, req.Id); readErr == nil {
		for _, ci := range current.ContactInfos {
			if ci.Type == models.ContactTypePhone {
				audit.phone = ci.Phone
			}
		}
	}

	updatedUser, err := s.userDBservice.DeletePhoneNumber(req.InstanceId, req.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	s.whatsAppQueue.CancelInBackground(req.InstanceId, req.Id, nil, false)

	return updatedUser.ToAPI(), nil
}

func (s *userManagementServer) VerifyWhatsAppCode(ctx context.Context, req *api.VerifyWhatsAppCodeReq) (_ *api.User, err error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.Code == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	audit := s.newPhoneAudit(req.Token, LOG_EVENT_PHONE_VERIFICATION, "", "verified")
	defer func() { audit.record(err) }()

	// A code that cannot be verified at all — none pending, or expired — is answered before
	// the counter is touched. The attempts exist to stop someone guessing a six digit code,
	// and refusing these two cases tells a guesser nothing; spending an attempt on them only
	// takes the budget away from the participant, who loses the registered number once it
	// runs out.
	pending, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}
	if pending.Account.PhoneVerificationCode.CodeHash == "" {
		return nil, status.Error(codes.InvalidArgument, "no phone verification in progress")
	}
	if time.Now().Unix() > pending.Account.PhoneVerificationCode.ExpiresAt {
		return nil, status.Error(codes.PermissionDenied, "verification code expired")
	}
	// A code proves control of the number it was sent to and of no other. When the account no
	// longer has that number waiting to be verified — it was replaced or deleted while the code
	// was in flight, or the code was stored before codes carried their destination — there is
	// nothing this code can confirm. The answer is the one given when no verification is in
	// progress, which is what this is: it separates neither case from the other, and it spends
	// no attempt, for the same reason an expired code spends none.
	sentTo := pending.Account.PhoneVerificationCode.Phone
	audit.phone = sentTo
	if sentTo == "" {
		return nil, status.Error(codes.InvalidArgument, "no phone verification in progress")
	}
	// Scanned here rather than through FindContactInfoByTypeAndAddr so that this guard states
	// the same condition as the two database filters do: a phone contact, with this number,
	// not yet confirmed.
	stillPending := false
	for _, ci := range pending.ContactInfos {
		if ci.Type == models.ContactTypePhone && ci.Phone == sentTo && ci.ConfirmedAt <= 0 {
			stillPending = true
			break
		}
	}
	if !stillPending {
		return nil, status.Error(codes.InvalidArgument, "no phone verification in progress")
	}

	// Digested before an attempt is spent, so a server that cannot digest codes answers with an
	// error and leaves the participant's budget alone.
	submitted, err := tokens.PhoneCodeDigest(req.Token.Id, req.Code)
	if err != nil {
		logger.Error.Printf("VerifyWhatsAppCode: %v", err)
		return nil, status.Error(codes.Internal, "could not check the verification code")
	}

	// Atomically increment attempts via $inc with a filter that caps at max.
	// This prevents race conditions: concurrent requests each get a distinct counter value.
	user, err := s.userDBservice.IncrementVerificationCodeAttempts(
		req.Token.InstanceId, req.Token.Id, s.Intervals.MaxVerificationAttempts,
	)
	if err == mongo.ErrNoDocuments {
		// Limit reached — remove the phone with the targeted delete, which also drops the
		// whatsapp channel and clears the code, instead of saving a user read a moment ago:
		// a full document save would carry a stale attempts ledger and drop the slots
		// reserved by concurrent sends.
		audit.outcome = "removed_after_failed_attempts"
		if _, updErr := s.userDBservice.DeletePhoneNumber(req.Token.InstanceId, req.Token.Id); updErr != nil {
			logger.Error.Printf("VerifyWhatsAppCode: failed to remove phone: %v", updErr)
			audit.outcome = "removal_after_failed_attempts_failed"
		} else {
			s.whatsAppQueue.CancelInBackground(req.Token.InstanceId, req.Token.Id, nil, false)
		}
		return nil, status.Error(codes.PermissionDenied, "too many attempts, phone number removed")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	// Compare against the document the increment returned rather than the one read above: a
	// resend running in between replaces the code, and the participant is typing the new one.
	if !tokens.SamePhoneCodeDigest(user.Account.PhoneVerificationCode.CodeHash, submitted) {
		return nil, status.Error(codes.PermissionDenied, "invalid verification code")
	}

	// Code correct: mark the phone as verified, enable the whatsapp channel and clear the spent
	// code, in one atomic write. Only whatsapp is added: the e-mail channel stays as the
	// participant left it. The channel list is computed by the database from what is stored, so
	// a concurrent change of the notification preferences is not overwritten by the list this
	// request read.
	updatedUser, err := s.userDBservice.FinalizePhoneVerification(req.Token.InstanceId, req.Token.Id, user.Account.PhoneVerificationCode.Phone)
	if err == mongo.ErrNoDocuments {
		// The number moved between the check above and this write. Nothing was confirmed and no
		// channel was enabled; the answer stays the one given for a code with nothing to verify.
		return nil, status.Error(codes.InvalidArgument, "no phone verification in progress")
	}
	if errors.Is(err, userdb.ErrPhoneVerifiedElsewhere) {
		// Another account proved the same number first and holds it; this one stays unverified.
		return nil, status.Error(codes.InvalidArgument, "phone number already taken")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// Control of the number has now been proved, which settles every competing claim to it: any
	// other account carrying it unverified was asserting it, and that assertion used to hold the
	// number against its real owner for good. This is the moment the question is decided, and the
	// only moment it can be decided in the participant's favour.
	// A failure is logged and not returned: the verification itself succeeded and is already
	// persisted, the claims it leaves behind are stale either way, and telling the participant
	// their own verification failed would be both wrong and not something they can act on.
	released, releaseErr := s.userDBservice.ReleaseUnverifiedPhoneClaims(req.Token.InstanceId, user.Account.PhoneVerificationCode.Phone, req.Token.Id)
	if releaseErr != nil {
		logger.Error.Printf("VerifyWhatsAppCode: failed to release unverified claims to the verified number: %v", releaseErr)
	} else if released > 0 {
		logger.Info.Printf("VerifyWhatsAppCode: released %d unverified claim(s) to a number that has now been verified", released)
	}

	return updatedUser.ToAPI(), nil
}
