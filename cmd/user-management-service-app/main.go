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

	userDBService := userdb.NewUserDBService(conf.UserDBConfig)
	globalDBService := globaldb.NewGlobalDBService(conf.GlobalDBConfig)

	// Start timer thread
	userTimerService := timer_event.NewUserManagmentTimerService(
		userManagementTimerEventFrequency,
		globalDBService,
		userDBService,
		clients,
		conf.CleanUpUnverifiedUsersAfter,
		conf.ReminderToUnverifiedAccountsAfter,
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
	); err != nil {
		logger.Error.Fatal(err)
	}
}
