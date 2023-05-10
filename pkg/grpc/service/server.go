package service

import (
	"context"
	"net"
	"os"
	"os/signal"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/dbs/globaldb"
	"github.com/influenzanet/user-management-service/pkg/dbs/userdb"
	itc "github.com/influenzanet/user-management-service/pkg/grpc/interceptors"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/utils"
	"google.golang.org/grpc"
)

const (
	// apiVersion is version of API is provided by server
	apiVersion = "v1"
)

type userManagementServer struct {
	api.UnimplementedUserManagementApiServer
	clients           *models.APIClients
	userDBservice     *userdb.UserDBService
	globalDBService   *globaldb.GlobalDBService
	Intervals         models.Intervals
	newUserCountLimit int64
	weekdayStrategy   utils.WeekDayStrategy
	instanceIDs       []string
}

// NewUserManagementServer creates a new service instance
func NewUserManagementServer(
	clients *models.APIClients,
	userDBservice *userdb.UserDBService,
	globalDBservice *globaldb.GlobalDBService,
	intervals models.Intervals,
	newUserCountLimit int64,
	weekdayStrategy utils.WeekDayStrategy,
	instanceIDs []string,
) api.UserManagementApiServer {
	return &userManagementServer{
		clients:           clients,
		userDBservice:     userDBservice,
		globalDBService:   globalDBservice,
		Intervals:         intervals,
		newUserCountLimit: newUserCountLimit,
		weekdayStrategy:   weekdayStrategy,
		instanceIDs:       instanceIDs,
	}
}

// RunServer runs gRPC service to publish ToDo service
func RunServer(ctx context.Context, port string,
	clients *models.APIClients,
	userDBservice *userdb.UserDBService,
	globalDBservice *globaldb.GlobalDBService,
	intervals models.Intervals,
	newUserCountLimit int64,
	weekdayStrategy utils.WeekDayStrategy,
	instanceIDs []string,
) error {
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		logger.Error.Fatalf("failed to listen: %v", err)
	}

	// register service
	server := grpc.NewServer(grpc.UnaryInterceptor(itc.InstanceIdInterceptor(instanceIDs)))
	api.RegisterUserManagementApiServer(server, NewUserManagementServer(
		clients,
		userDBservice,
		globalDBservice,
		intervals,
		newUserCountLimit,
		weekdayStrategy,
		instanceIDs,
	))

	// graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for range c {
			// sig is a ^C, handle it
			logger.Debug.Println("shutting down gRPC server...")
			server.GracefulStop()
			<-ctx.Done()
		}
	}()

	// start gRPC server
	logger.Debug.Println("starting gRPC server...")
	logger.Debug.Println("wait connections on port " + port)
	return server.Serve(lis)
}
