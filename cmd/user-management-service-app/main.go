package main

import (
	"context"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/dbs/globaldb"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	gc "github.com/influenzanet/user-management-service/pkg/grpc/clients"
	"github.com/influenzanet/user-management-service/pkg/grpc/service"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/timer_event"
)

const userManagementTimerEventFrequency = 90 * 60 // seconds

func main() {
	conf := config.InitConfig()

	logger.SetLevel(conf.LogLevel)

	clients := &models.APIClients{}

	messagingClient, close := gc.ConnectToMessagingService(conf.ServiceURLs.MessagingService)
	defer close()
	clients.MessagingService = messagingClient

	loggingClient, close := gc.ConnectToLoggingService(conf.ServiceURLs.LoggingService)
	defer close()
	clients.LoggingService = loggingClient

	studyClient, close := gc.ConnectToStudyService(conf.ServiceURLs.LoggingService)
	defer close()
	clients.StudyService = studyClient

	userDBService := userdb.NewUserDBService(conf.UserDBConfig)
	globalDBService := globaldb.NewGlobalDBService(conf.GlobalDBConfig)

	// Read instance ID list
	instanceIDObjects, err := globalDBService.GetAllInstances()
	if err != nil {
		logger.Error.Fatalf("Couldn't read instance IDs: %v", err)
	}
	if len(instanceIDObjects) == 0 {
		logger.Error.Fatal("No instance ID found in the database.")
	}
	instanceIDs := []string{}
	for _, instanceIDObject := range instanceIDObjects {
		instanceIDs = append(instanceIDs, instanceIDObject.InstanceID)
	}

	// Ensure indexes
	ensureDBIndexes(instanceIDs, userDBService)

	// Start timer thread
	userTimerService := timer_event.NewUserManagmentTimerService(
		userManagementTimerEventFrequency,
		globalDBService,
		userDBService,
		clients,
		conf.CleanUpUnverifiedUsersAfter,
		conf.ReminderToUnverifiedAccountsAfter,
		conf.DeleteAccountAfterNotifyingUser,
		conf.DeleteAccountAfterNotifyingUser,
	)

	// Start server thread
	ctx := context.Background()

	userTimerService.Run(ctx)

	if err := service.RunServer(
		ctx,
		conf.Port,
		clients,
		userDBService,
		globalDBService,
		conf.Intervals,
		conf.NewUserCountLimit,
		conf.WeekDayStrategy,
		instanceIDs,
	); err != nil {
		logger.Error.Fatal(err)
	}
}

func ensureDBIndexes(instanceIDs []string, udb *userdb.UserDBService) {
	for _, i := range instanceIDs {
		logger.Debug.Printf("ensuring indexes for instance %s", i)

		udb.CreateIndexForRenewTokens(i)
		// TODO: ensure index for users collection as well
	}
}
