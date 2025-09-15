package service

import (
	"context"
	"time"

	"github.com/coneno/logger"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
)

func (s *userManagementServer) SaveLogEvent(
	instanceID string,
	userID string,
	eventType loggingAPI.LogEventType,
	eventName string,
	msg string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := s.clients.LoggingService.SaveLogEvent(ctx, &loggingAPI.NewLogEvent{
		Origin:     "user-management",
		InstanceId: instanceID,
		UserId:     userID,
		EventType:  eventType,
		EventName:  eventName,
		Msg:        msg,
	})
	if err != nil {
		logger.Error.Printf("failed to save log: %s", err.Error())
	}
}
