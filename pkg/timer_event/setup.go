package timer_event

import (
	"context"
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/dbs/globaldb"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	"github.com/influenzanet/user-management-service/pkg/models"
)

// UserManagementTimerService handles background times for user management (cleanup for example).
type UserManagementTimerService struct {
	globalDBService                      *globaldb.GlobalDBService
	userDBService                        *userdb.UserDBService
	clients                              *models.APIClients
	TimerEventFrequency                  int64 // how often the timer event should be performed (only from one instance of the service) - seconds
	CleanUpTimeThreshold                 int64 // if user account not verified, remove user after this many seconds
	ReminderTimeThreshold                int64 // if user account not verified, send a reminder email to the user after this many seconds
	NotifyInactiveUserThreshold          int64 // if user account is inactive, send a reminder email to the user after this many seconds
	DeleteAccountAfterNotifyingThreshold int64 // if user account is notified by mail, delete account after this many seconds

}

func NewUserManagmentTimerService(
	frequency int64,
	globalDBService *globaldb.GlobalDBService,
	userDBService *userdb.UserDBService,
	clients *models.APIClients,
	cleanUpTimeThreshold int64,
	reminderTimeThreshold int64,
	notifyInactiveUserThreshold int64,
	deleteAccountAfterNotifyingThreshold int64,
) *UserManagementTimerService {
	return &UserManagementTimerService{
		globalDBService:                      globalDBService,
		userDBService:                        userDBService,
		TimerEventFrequency:                  frequency,
		clients:                              clients,
		CleanUpTimeThreshold:                 cleanUpTimeThreshold,
		ReminderTimeThreshold:                reminderTimeThreshold,
		NotifyInactiveUserThreshold:          notifyInactiveUserThreshold,
		DeleteAccountAfterNotifyingThreshold: deleteAccountAfterNotifyingThreshold,
	}
}

func (s *UserManagementTimerService) Run(ctx context.Context) {
	go s.startTimerThread(ctx, s.TimerEventFrequency)
}

func (s *UserManagementTimerService) startTimerThread(ctx context.Context, timeCheckInterval int64) {
	logger.Info.Printf("Starting timer thread with frequency %d seconds", timeCheckInterval)
	for {
		select {
		case <-time.After(time.Duration(timeCheckInterval) * time.Second):
			go s.CleanUpUnverifiedUsers()
			go s.ReminderToConfirmAccount()
			go s.DetectAndNotifyInactiveUsers()
			go s.CleanupUsersMarkedForDeletion()
		case <-ctx.Done():
			return
		}
	}
}
