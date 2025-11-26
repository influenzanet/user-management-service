package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/go-utils/pkg/constants"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/pwhash"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *userManagementServer) CreateUser(ctx context.Context, req *api.CreateUserReq) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.AccountId == "" || req.InitialPassword == "" {
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}
	if !utils.CheckRoleInToken(req.Token, constants.USER_ROLE_ADMIN) {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}

	req.AccountId = utils.SanitizeEmail(req.AccountId)
	if !utils.CheckEmailFormat(req.AccountId) {
		return nil, status.Error(codes.InvalidArgument, "account id not a valid email")
	}
	if !utils.CheckPasswordFormat(req.InitialPassword) {
		return nil, status.Error(codes.InvalidArgument, "password too weak")
	}

	password, err := pwhash.HashPassword(req.InitialPassword)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	accountCreatedAt := time.Now().Unix() + userCreationTimestampOffset
	if req.CreatedAt > 0 {
		accountCreatedAt = req.CreatedAt
	}

	// Create user DB object from request:
	newUser := models.User{
		Account: models.Account{
			Type:               "email",
			AccountID:          req.AccountId,
			AccountConfirmedAt: req.AccountConfirmedAt,
			Password:           password,
			PreferredLanguage:  req.PreferredLanguage,
		},
		Roles:    req.Roles,
		Profiles: []models.Profile{},
		Timestamps: models.Timestamps{
			CreatedAt: accountCreatedAt,
		},
	}

	// Init profiles:
	if len(req.ProfileNames) < 1 {
		newUser.Profiles = append(newUser.Profiles, models.Profile{
			ID:                 primitive.NewObjectID(),
			Alias:              utils.BlurEmailAddress(req.AccountId),
			AvatarID:           "default",
			ConsentConfirmedAt: time.Now().Unix(),
			MainProfile:        true,
		})
	} else {
		for i, pn := range req.ProfileNames {
			newUser.Profiles = append(newUser.Profiles, models.Profile{
				ID:                 primitive.NewObjectID(),
				Alias:              pn,
				AvatarID:           "default",
				ConsentConfirmedAt: time.Now().Unix(),
				MainProfile:        i == 0,
			})
		}
	}

	newUser.AddNewEmail(req.AccountId, false)
	if req.Use_2Fa {
		newUser.Account.AuthType = "2FA"
	}
	newUser.ContactPreferences.SubscribedToNewsletter = false
	newUser.ContactPreferences.SendNewsletterTo = []string{newUser.ContactInfos[0].ID.Hex()}
	newUser.ContactPreferences.SubscribedToWeekly = false
	newUser.ContactPreferences.ReceiveWeeklyMessageDayOfWeek = int32(s.weekdayStrategy.Weekday())

	instanceID := req.Token.InstanceId
	id, err := s.userDBservice.AddUser(instanceID, newUser)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	newUser.ID, _ = primitive.ObjectIDFromHex(id)

	// TempToken for contact verification:
	tempTokenInfos := models.TempToken{
		UserID:     id,
		InstanceID: instanceID,
		Purpose:    constants.TOKEN_PURPOSE_INVITATION,
		Info: map[string]string{
			"type":  "email",
			"email": newUser.Account.AccountID,
		},
		Expiration: tokens.GetExpirationTime(s.Intervals.InvitationTokenLifetime),
	}
	tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// ---> Trigger message sending
	_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
		InstanceId:  instanceID,
		To:          []string{newUser.Account.AccountID},
		MessageType: constants.EMAIL_TYPE_INVITATION,
		ContentInfos: map[string]string{
			"token": tempToken,
		},
		PreferredLanguage: newUser.Account.PreferredLanguage,
		UseLowPrio:        true,
	})
	if err != nil {
		logger.Error.Printf("CreateUser: %s", err.Error())
	}
	// <---

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_ACCOUNT_CREATED, "by admin - "+newUser.ID.Hex()+" - "+newUser.Account.AccountID)

	return newUser.ToAPI(), nil
}

func (s *userManagementServer) AddRoleForUser(ctx context.Context, req *api.RoleMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.AccountId == "" || req.Role == "" {
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}
	if !utils.CheckRoleInToken(req.Token, constants.USER_ROLE_ADMIN) {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}

	user, err := s.userDBservice.GetUserByAccountID(req.Token.InstanceId, req.AccountId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := user.AddRole(req.Role); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	user, err = s.userDBservice.UpdateUser(req.Token.InstanceId, user)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_ACCOUNT_ROLE_ADDED, user.Account.AccountID+"("+user.ID.Hex()+") + "+req.Role)

	return user.ToAPI(), nil
}

func (s *userManagementServer) RemoveRoleForUser(ctx context.Context, req *api.RoleMsg) (*api.User, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) || req.AccountId == "" || req.Role == "" {
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}
	if !utils.CheckRoleInToken(req.Token, constants.USER_ROLE_ADMIN) {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	user, err := s.userDBservice.GetUserByAccountID(req.Token.InstanceId, req.AccountId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := user.RemoveRole(req.Role); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	user, err = s.userDBservice.UpdateUser(req.Token.InstanceId, user)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.SaveLogEvent(req.Token.InstanceId, req.Token.Id, loggingAPI.LogEventType_LOG, constants.LOG_EVENT_ACCOUNT_ROLE_REMOVED, user.Account.AccountID+"("+user.ID.Hex()+") - "+req.Role)
	return user.ToAPI(), nil
}

func (s *userManagementServer) FindNonParticipantUsers(ctx context.Context, req *api.FindNonParticipantUsersMsg) (*api.UserListMsg, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) {
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}
	if !utils.CheckRoleInToken(req.Token, constants.USER_ROLE_ADMIN) {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}

	users, err := s.userDBservice.FindNonParticipantUsers(req.Token.InstanceId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	resp := api.UserListMsg{
		Users: make([]*api.User, len(users)),
	}
	for i, u := range users {
		resp.Users[i] = u.ToAPI()
	}
	return &resp, nil
}

func (s *userManagementServer) StreamUsers(req *api.StreamUsersMsg, stream api.UserManagementApi_StreamUsersServer) error {
	if req == nil || stream == nil || req.InstanceId == "" {
		return status.Error(codes.InvalidArgument, "missing arguments")
	}

	ctx := context.Background()

	sendUserOverGrpc := func(instanceID string, user models.User, args ...interface{}) error {
		if len(args) != 1 {
			return errors.New("StreamUsers callback: unexpected number of args")
		}
		stream, ok := args[0].(api.UserManagementApi_StreamUsersServer)
		if !ok {
			return errors.New(("StreamUsers callback: can't parse stream"))
		}

		if err := stream.Send(user.ToAPI()); err != nil {
			logger.Error.Printf("unexpected error when sending user object: %v", err)
			return err
		}
		return nil
	}

	filter := userdb.UserFilter{
		OnlyConfirmed:   false,
		ReminderWeekDay: -1,
	}
	if req.Filters != nil {
		filter.OnlyConfirmed = req.Filters.OnlyConfirmedAccounts
		if req.Filters.UseReminderWeekdayFilter {
			filter.ReminderWeekDay = req.Filters.ReminderWeekday
		}
	}

	err := s.userDBservice.PerfomActionForUsers(ctx, req.InstanceId, filter, sendUserOverGrpc, stream)
	if err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	return nil
}

func (s *userManagementServer) GetUserContactPreferences(ctx context.Context, req *api.UserReference) (*api.ContactPreferencesResponse, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}

	// 1. Find user by token ID
	user, err := s.userDBservice.GetUserByID(req.Token.InstanceId, req.Token.Id)
	if err != nil {
		return nil, status.Error(codes.Internal, "user not found")
	}

	// 2. Extract contact information (email and phone)
	var email, phone string
	for _, ci := range user.ContactInfos {
		if ci.Type == "email" {
			email = ci.Email
		} else if ci.Type == "phone" {
			phone = ci.Phone
		}
	}

	// 3. Build and return response
	return &api.ContactPreferencesResponse{
		UserId:            user.ID.Hex(),
		Email:             email,
		PhoneNumber:       phone,
		PreferredChannels: user.Account.NotificationChannels,
	}, nil
}

func (s *userManagementServer) SendMessage(ctx context.Context, req *api.SendMessageRequest) (*api.ServiceStatus, error) {
	// Add security checks here, e.g., if call comes from trusted service
	if req == nil || req.ToPhoneNumber == "" || req.MessageType == "" {
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}

	// Determine which template to use based on message type
	var templateName, templateLang string
	switch req.MessageType {
	case "weekly_reminder":
		templateName = s.whatsAppConfig.WeeklyReminderTemplateName
		templateLang = s.whatsAppConfig.WeeklyReminderTemplateLang
		if templateName == "" {
			logger.Warning.Printf("weekly_reminder template not configured, falling back to text message")
			// Fallback to text message
			messageBody := "Promemoria settimanale: compila il questionario su orbyta.influenzanet.info"
			err := s.whatsAppClient.SendTextMessage(req.ToPhoneNumber, messageBody)
			if err != nil {
				logger.Error.Printf("failed to send whatsapp message: %v", err)
				return nil, status.Error(codes.Internal, "failed to send message")
			}
			return &api.ServiceStatus{Status: api.ServiceStatus_NORMAL, Msg: "message sent (text fallback)"}, nil
		}
	default:
		// Unknown message type - send as text message
		logger.Warning.Printf("Unknown message type '%s', sending as text message", req.MessageType)
		messageBody := fmt.Sprintf("Message type '%s' with params: %v", req.MessageType, req.ContentParams)
		err := s.whatsAppClient.SendTextMessage(req.ToPhoneNumber, messageBody)
		if err != nil {
			logger.Error.Printf("failed to send whatsapp message: %v", err)
			return nil, status.Error(codes.Internal, "failed to send message")
		}
		return &api.ServiceStatus{Status: api.ServiceStatus_NORMAL, Msg: "message sent"}, nil
	}

	// Use req.Lang if provided, otherwise use default from config
	if req.Lang != "" {
		templateLang = req.Lang
	}

	// Send using template
	err := s.whatsAppClient.SendTemplateMessage(req.ToPhoneNumber, templateName, templateLang, req.ContentParams)
	if err != nil {
		logger.Error.Printf("failed to send whatsapp template message: %v", err)
		return nil, status.Error(codes.Internal, "failed to send message")
	}

	return &api.ServiceStatus{Status: api.ServiceStatus_NORMAL, Msg: "message sent"}, nil
}
