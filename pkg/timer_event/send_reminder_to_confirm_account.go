package timer_event

import (
	"context"
	"errors"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/go-utils/pkg/constants"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
)

// CleanUpUnverifiedUsers handles the deletion of unverified accounts after a threshold delay
func (s *UserManagementTimerService) ReminderToConfirmAccount() {
	logger.Debug.Println("Check if reminders to confirm accounts need to be sent out.")
	instances, err := s.globalDBService.GetAllInstances()
	if err != nil {
		logger.Error.Printf("unexpected error: %s", err.Error())
	}
	sendReminderToConfirmAfter := s.ReminderTimeThreshold

	sendReminderToUser := func(instanceID string, user models.User, args ...interface{}) error {
		count, _ := args[0].(*int)

		tempTokenInfos := models.TempToken{
			UserID:     user.ID.Hex(),
			InstanceID: instanceID,
			Purpose:    constants.TOKEN_PURPOSE_CONTACT_VERIFICATION,
			Info: map[string]string{
				"type":  models.ACCOUNT_TYPE_EMAIL,
				"email": user.Account.AccountID,
			},
			Expiration: tokens.GetExpirationTime(time.Hour * 24 * 30),
		}
		tempToken, err := s.globalDBService.AddTempToken(tempTokenInfos)
		if err != nil {
			logger.Error.Printf("unexpected error: %s", err.Error())
			return errors.New("failed to create verification token")
		}

		// ---> Trigger message sending

		_, err = s.clients.MessagingService.SendInstantEmail(context.TODO(), &messageAPI.SendEmailReq{
			InstanceId:  instanceID,
			To:          []string{user.Account.AccountID},
			MessageType: constants.EMAIL_TYPE_REGISTRATION,
			ContentInfos: map[string]string{
				"token": tempToken,
			},
			PreferredLanguage: user.Account.PreferredLanguage,
		})
		if err != nil {
			logger.Error.Printf("unexpected error: %s", err.Error())
			return err
		}
		*count = *count + 1
		return nil
	}

	for _, instance := range instances {
		count := 0
		ctx := context.Background()
		err := s.userDBService.SendReminderToConfirmAccountLoop(ctx, instance.InstanceID, time.Now().Unix()-sendReminderToConfirmAfter, sendReminderToUser, &count)
		if err != nil {
			logger.Error.Printf("unexpected error: %s", err.Error())
			continue
		}
		if count > 0 {
			logger.Info.Printf("%s: %d sent reminders to unverified accounts", instance.InstanceID, count)
		} else {
			logger.Debug.Printf("%s: %d sent reminders to unverified accounts", instance.InstanceID, count)
		}

	}
}
