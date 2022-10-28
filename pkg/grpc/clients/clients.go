package clients

import (
	"github.com/coneno/logger"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	"google.golang.org/grpc"
)

func connectToGRPCServer(addr string) *grpc.ClientConn {
	conn, err := grpc.Dial(addr, grpc.WithInsecure())
	if err != nil {
		logger.Error.Fatalf("failed to connect to %s: %v", addr, err)
	}
	return conn
}

func ConnectToMessagingService(addr string) (client messageAPI.MessagingServiceApiClient, close func() error) {
	// Connect to user management service
	serverConn := connectToGRPCServer(addr)
	return messageAPI.NewMessagingServiceApiClient(serverConn), serverConn.Close
}

func ConnectToLoggingService(addr string) (client loggingAPI.LoggingServiceApiClient, close func() error) {
	// Connect to user management service
	serverConn := connectToGRPCServer(addr)
	return loggingAPI.NewLoggingServiceApiClient(serverConn), serverConn.Close
}
