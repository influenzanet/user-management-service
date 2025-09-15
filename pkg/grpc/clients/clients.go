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

// WhatsAppClient handles communication with the WhatsApp Business API
type WhatsAppClient struct {
	httpClient    *http.Client
	apiToken      string
	phoneNumberID string
	templateName  string
}

// NewWhatsAppClient creates a new client instance
func NewWhatsAppClient(token, phoneID, templateName string) *WhatsAppClient {
	return &WhatsAppClient{
		httpClient:    &http.Client{},
		apiToken:      token,
		phoneNumberID: phoneID,
		templateName:  templateName,
	}
}

// SendVerificationCode sends a verification code using a pre-approved template
func (c *WhatsAppClient) SendVerificationCode(toPhoneNumber, code, lang string) error {
	apiURL := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/messages", c.phoneNumberID)

	// Base template structure
	// Builds the base template
	template := map[string]interface{}{
		"name": c.templateName,
		"language": map[string]string{
			"code": lang,
		},
	}

	// Try first without parameters for simple templates
	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                toPhoneNumber,
		"type":              "template",
		"template":          template,
	}

	// Attempt sending without parameters
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

	logger.Info.Printf("WhatsApp SendVerificationCode -> to:%s lang:%s template:%s (trying without parameters)", toPhoneNumber, lang, c.templateName)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	// If it works without parameters, return success
	if resp.StatusCode < 300 {
		logger.Info.Println("WhatsApp SendVerificationCode: delivered to API (no parameters)")
		return nil
	}

	// If it fails, try with parameters
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

	// Rebuild the payload with parameters
	payload = map[string]interface{}{
		"messaging_product": "whatsapp",
		"to":                toPhoneNumber,
		"type":              "template",
		"template":          template,
	}

	body, err = json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err = http.NewRequest("POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	logger.Info.Printf("WhatsApp SendVerificationCode -> to:%s lang:%s template:%s (with parameters)", toPhoneNumber, lang, c.templateName)
	resp, err = c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var respObj map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&respObj)
		logger.Error.Printf("WhatsApp SendVerificationCode failed status=%d resp=%v", resp.StatusCode, respObj)
		logger.Error.Printf("WhatsApp Request payload was: %s", string(body))
		return fmt.Errorf("failed to send message, status code: %d", resp.StatusCode)
	}
	logger.Info.Println("WhatsApp SendVerificationCode: delivered to API (with parameters)")

	return nil
}

// SendTextMessage sends a simple text message
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
