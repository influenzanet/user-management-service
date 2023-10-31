package timer_event

import (
	"context"

	"github.com/coneno/logger"
	"github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/go-utils/pkg/constants"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/pkg/utils"
)

// CleanupUsersMarkedForDeletion handles the deletion of accounts that did not react to reminder mail
func (s *UserManagementTimerService) CleanupUsersMarkedForDeletion() {
	logger.Debug.Println("Starting clean up job for users marked for deletion:")
	instances, err := s.globalDBService.GetAllInstances()
	if err != nil {
		logger.Error.Printf("unexpected error: %s", err.Error())
	}
	for _, instance := range instances {
		users, err := s.userDBService.FindUsersMarkedForDeletion(instance.InstanceID)
		count := 0

		if err != nil {
			logger.Error.Printf("unexpected error: %s", err.Error())
			continue
		}
		for _, u := range users {
			// ---> Trigger message sending
			_, err = s.clients.MessagingService.SendInstantEmail(context.TODO(), &messageAPI.SendEmailReq{
				InstanceId:        instance.InstanceID,
				To:                []string{u.Account.AccountID},
				MessageType:       constants.EMAIL_TYPE_ACCOUNT_DELETED_AFTER_INACTIVITY,
				PreferredLanguage: u.Account.PreferredLanguage,
				UseLowPrio:        true,
			})
			if err != nil {
				logger.Error.Printf("DeleteAccount: %s", err.Error())
			}
			err := s.globalDBService.DeleteAllTempTokenForUser(instance.InstanceID, u.ID.Hex(), "")
			if err != nil {
				logger.Error.Printf("error, when trying to remove temp-tokens: %s", err.Error())
				continue
			}
			_, err = s.userDBService.DeleteRenewTokensForUser(instance.InstanceID, u.ID.Hex())
			if err != nil {
				logger.Error.Printf("error, when trying to remove renew tokens: %s", err.Error())
				continue
			}
			err = s.userDBService.DeleteUser(instance.InstanceID, u.ID.Hex())
			if err != nil {
				logger.Error.Printf("error, when trying to delete user: %s", err.Error())
				continue
			}

			//notify study service
			mainProfileID, otherProfileIDs := utils.GetMainAndOtherProfiles(u)
			userProfileIDs := []string{mainProfileID}
			userProfileIDs = append(userProfileIDs, otherProfileIDs...)
			token := &api_types.TokenInfos{
				Id:              u.ID.Hex(),
				InstanceId:      instance.InstanceID,
				ProfilId:        mainProfileID,
				OtherProfileIds: otherProfileIDs,
			}
			for _, profileId := range userProfileIDs {
				token.ProfilId = profileId
				if _, err := s.clients.StudyService.ProfileDeleted(context.Background(), token); err != nil {
					logger.Error.Printf("failed to notify study service: %s", err.Error())
				}
			}

			_, err = s.clients.LoggingService.SaveLogEvent(context.TODO(), &loggingAPI.NewLogEvent{
				Origin:     "user-management",
				InstanceId: instance.InstanceID,
				UserId:     u.ID.Hex(),
				EventType:  loggingAPI.LogEventType_LOG,
				EventName:  constants.LOG_EVENT_ACCOUNT_DELETED_AFTER_INACTIVITY,
				Msg:        u.Account.AccountID,
			})
			if err != nil {
				logger.Error.Printf("failed to save log: %s", err.Error())
			}
			logger.Info.Printf("%s: removed account with user ID %s", instance.InstanceID, u.ID.Hex())
			count++

		}
		if count > 0 {
			logger.Info.Printf("%s: removed %d inactive accounts", instance.InstanceID, count)
		} else {
			logger.Debug.Printf("%s: removed %d inactive accounts", instance.InstanceID, count)
		}

	}
}
