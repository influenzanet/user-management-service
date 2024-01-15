package timer_event

import (
	"context"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/go-utils/pkg/constants"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
)

func (s *UserManagementTimerService) DetectAndNotifyInactiveUsers() {

	logger.Debug.Println("Starting search and notify job for inactive users:")
	instances, err := s.globalDBService.GetAllInstances()
	if err != nil {
		logger.Error.Printf("unexpected error: %s", err.Error())
	}

	for _, instance := range instances {

		users, err := s.userDBService.FindInactiveUsers(instance.InstanceID, s.NotifyInactiveUserThreshold)
		count := 0
		if err != nil {
			logger.Error.Printf("unexpected error: %s", err.Error())
			continue
		}

		for _, u := range users {
			tempTokenInfos := models.TempToken{
				UserID:     u.ID.Hex(),
				InstanceID: instance.InstanceID,
				Purpose:    constants.TOKEN_PURPOSE_INACTIVE_USER_NOTIFICATION,
				Info: map[string]string{
					"type":  models.ACCOUNT_TYPE_EMAIL,
					"email": u.Account.AccountID,
				},
				Expiration: tokens.GetExpirationTime(time.Second * time.Duration(s.DeleteAccountAfterNotifyingThreshold)),
			}
			tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
			if err != nil {
				logger.Error.Printf("failed to create verification token: %s", err.Error())
				continue
			}
			//send message
			// ---> Trigger message sending
			_, err = s.clients.MessagingService.QueueEmailTemplateForSending(context.TODO(), &messageAPI.SendEmailReq{
				InstanceId:  instance.InstanceID,
				To:          []string{u.Account.AccountID},
				MessageType: constants.EMAIL_TYPE_ACCOUNT_INACTIVITY,
				ContentInfos: map[string]string{
					"token": tempToken,
				},
				PreferredLanguage: u.Account.PreferredLanguage,
			})
			if err != nil {
				logger.Error.Printf("unexpected error: %v", err)
				continue
			}
			succcess, err := s.userDBService.UpdateMarkedForDeletionTime(instance.InstanceID, u.ID.Hex(), s.DeleteAccountAfterNotifyingThreshold, false)
			if err != nil {
				logger.Error.Printf("unexpected error: %v", err)
				continue
			}
			if !succcess { //markedForDeletion already set by other service
				continue
			}
			count++
		}
		if count > 0 {
			logger.Info.Printf("%s: notification mail will be sent to %d inactive accounts", instance.InstanceID, count)
		} else {
			logger.Debug.Printf("%s: notification mail will be sent to %d inactive accounts", instance.InstanceID, count)
		}
	}
}
