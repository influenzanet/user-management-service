package timer_event

import (
	"github.com/coneno/logger"
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
			err := s.globalDBService.DeleteAllTempTokenForUser(instance.InstanceID, u.ID.Hex(), "")
			if err != nil {
				logger.Error.Printf("unexpected error: %s", err.Error())
				continue
			}
			_, err = s.userDBService.DeleteRenewTokensForUser(instance.InstanceID, u.ID.Hex())
			if err != nil {
				logger.Error.Printf("unexpected error: %s", err.Error())
				continue
			}
			err = s.userDBService.DeleteUser(instance.InstanceID, u.ID.Hex())
			if err != nil {
				logger.Error.Printf("unexpected error: %s", err.Error())
				continue
			}
			//TODO: notify study service

			//TODO: log event

		}
		if count > 0 {
			logger.Info.Printf("%s: removed %d inactive accounts", instance.InstanceID, count)
		} else {
			logger.Debug.Printf("%s: removed %d inactive accounts", instance.InstanceID, count)
		}

	}
}
