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

	// Base template structure
	template := map[string]interface{}{
		"name": c.templateName,
		"language": map[string]string{
			"code": lang,
		},
	}

	// Add components only for templates that require parameters
	// Templates like 'hello_world' don't need parameters, while 'verification' does
	if c.templateRequiresParameters() {
		template["components"] = []map[string]interface{}{
			{
				"type": "body",
				"parameters": []map[string]string{
					{"type": "text", "text": code},
				},
			},
			{
				"type":     "button",
				"sub_type": "url",
				"index":    "0",
				"parameters": []map[string]string{
					{"type": "text", "text": code},
				},
			},
		}
	}

	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                toPhoneNumber,
		"type":              "template",
		"template":          template,
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

	logger.Info.Printf("WhatsApp SendVerificationCode -> to:%s lang:%s template:%s", toPhoneNumber, lang, c.templateName)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var respObj map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&respObj)
		logger.Error.Printf("WhatsApp SendVerificationCode failed status=%d resp=%v", resp.StatusCode, respObj)
		return fmt.Errorf("failed to send message, status code: %d", resp.StatusCode)
	}
	logger.Info.Println("WhatsApp SendVerificationCode: delivered to API")

	return nil
}

// templateRequiresParameters determina se un template richiede parametri
func (c *WhatsAppClient) templateRequiresParameters() bool {
	// Lista di template che non richiedono parametri (template semplici)
	simpleTemplates := []string{
		"hello_world",
		"welcome",
		"goodbye",
		"thank_you",
	}

	// Controlla se il template corrente è nella lista dei template semplici
	for _, simple := range simpleTemplates {
		if c.templateName == simple {
			return false
		}
	}

	// Default: i template richiedono parametri (per template come "verification")
	return true
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

	logger.Info.Printf("WhatsApp SendTextMessage -> to:%s", toPhoneNumber)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var respObj map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&respObj)
		logger.Error.Printf("WhatsApp SendTextMessage failed status=%d resp=%v", resp.StatusCode, respObj)
		return fmt.Errorf("failed to send message, status code: %d", resp.StatusCode)
	}
	logger.Info.Println("WhatsApp SendTextMessage: delivered to API")

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
