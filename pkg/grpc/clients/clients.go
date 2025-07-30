package clients

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/coneno/logger"
	loggingAPI "github.com/influenzanet/logging-service/pkg/api"
	messageAPI "github.com/influenzanet/messaging-service/pkg/api/messaging_service"
	studyAPI "github.com/influenzanet/study-service/pkg/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// WhatsAppClient gestisce la comunicazione con la WhatsApp Business API
type WhatsAppClient struct {
	httpClient    *http.Client
	apiToken      string
	phoneNumberID string
	templateName  string
}

// NewWhatsAppClient crea una nuova istanza del client
func NewWhatsAppClient(token, phoneID, templateName string) *WhatsAppClient {
	return &WhatsAppClient{
		httpClient:    &http.Client{},
		apiToken:      token,
		phoneNumberID: phoneID,
		templateName:  templateName,
	}
}

// SendVerificationCode invia un codice di verifica usando un template pre-approvato
func (c *WhatsAppClient) SendVerificationCode(toPhoneNumber, code, lang string) error {
	apiURL := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/messages", c.phoneNumberID)

	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                toPhoneNumber,
		"type":              "template",
		"template": map[string]interface{}{
			"name": c.templateName,
			"language": map[string]string{
				"code": lang,
			},
			"components": []map[string]interface{}{
				{
					"type": "body",
					"parameters": []map[string]string{
						{"type": "text", "text": code},
					},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		// Qui puoi leggere il body della risposta per un errore più dettagliato
		return fmt.Errorf("failed to send message, status code: %d", resp.StatusCode)
	}

	return nil
}

// SendTextMessage invia un messaggio di testo semplice
func (c *WhatsAppClient) SendTextMessage(toPhoneNumber, message string) error {
	apiURL := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/messages", c.phoneNumberID)

	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                toPhoneNumber,
		"type":              "text",
		"text": map[string]string{
			"body": message,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("failed to send message, status code: %d", resp.StatusCode)
	}

	return nil
}

func connectToGRPCServer(addr string) *grpc.ClientConn {
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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

func ConnectToStudyService(addr string) (client studyAPI.StudyServiceApiClient, close func() error) {
	// Connect to user management service
	serverConn := connectToGRPCServer(addr)
	return studyAPI.NewStudyServiceApiClient(serverConn), serverConn.Close
}
