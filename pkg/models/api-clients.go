package models

import (
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/grpc/clients"
)

// APIClients holds the service clients to the internal services
type APIClients struct {
	MessagingService messageAPI.MessagingServiceApiClient
	LoggingService   loggingAPI.LoggingServiceApiClient
	StudyService     studyAPI.StudyServiceApiClient
	WhatsApp         *clients.WhatsAppClient
}
