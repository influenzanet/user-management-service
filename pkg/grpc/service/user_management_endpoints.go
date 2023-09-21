package service

import (
	"context"
	"errors"
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

func (s *userManagementServer) DetectAndNotifyInactiveUsers(ctx context.Context, req *api.FindNonParticipantUsersMsg) (*api.UserListMsg, error) {
	if req == nil || utils.IsTokenEmpty(req.Token) { //TODO change request parameter
		return nil, status.Error(codes.InvalidArgument, "missing arguments")
	}
	if !utils.CheckRoleInToken(req.Token, constants.USER_ROLE_ADMIN) {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}

	dT1 := int64(1000) //TODO: set dT1
	dT2 := int64(100)
	users, err := s.userDBservice.FindInactiveUsers(req.Token.InstanceId, dT1)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	for _, u := range users {
		succcess, err := s.userDBservice.UpdateMarkedForDeletionTime(req.Token.InstanceId, u.Account.AccountID, dT2, false)
		if err != nil {
			logger.Error.Printf("unexpected error: %v", err)
			continue
		}
		if !succcess { //markedForDeletion already set by other service
			continue
		}
		//send message
		// ---> Trigger message sending
		_, err = s.clients.MessagingService.SendInstantEmail(ctx, &messageAPI.SendEmailReq{
			InstanceId:  req.Token.InstanceId,
			To:          []string{u.Account.AccountID},
			MessageType: "account-inactivity", //TODO: add to constants
			ContentInfos: map[string]string{
				"token": req.Token.Id, //TODO: correct token or define new one?
			},
			PreferredLanguage: u.Account.PreferredLanguage,
		})
		if err != nil {
			logger.Error.Printf("unexpected error: %v", err)
			s.userDBservice.UpdateMarkedForDeletionTime(req.Token.InstanceId, u.Account.AccountID, 0, true)
		}
	}
	resp := api.UserListMsg{ //TODO: what return value??
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
