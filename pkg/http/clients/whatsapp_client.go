package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coneno/logger"
)

const (
	whatsAppHTTPTimeout = 30 * time.Second
	whatsAppAPIBaseURL  = "https://graph.facebook.com/v19.0"

	// Meta Graph API error code: "Recipient phone number not in allowed list"
	// (returned e.g. for numbers not whitelisted while the WABA is in test mode)
	metaErrCodeRecipientNotAllowed = 131030
)

// ErrRecipientNotAllowed is returned when Meta rejects the recipient itself,
// so callers can surface a meaningful message instead of a generic send failure
var ErrRecipientNotAllowed = errors.New("recipient phone number not allowed by WhatsApp")

// parseSendError maps a non-2xx Meta response to an error, detecting known error codes
func parseSendError(statusCode int, respObj map[string]any) error {
	if errObj, ok := respObj["error"].(map[string]any); ok {
		if code, ok := errObj["code"].(float64); ok && int(code) == metaErrCodeRecipientNotAllowed {
			return ErrRecipientNotAllowed
		}
	}
	return fmt.Errorf("failed to send message, status code: %d", statusCode)
}

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
		httpClient:    &http.Client{Timeout: whatsAppHTTPTimeout},
		apiToken:      token,
		phoneNumberID: phoneID,
		templateName:  templateName,
	}
}

// mapLanguageCode converts system language codes to Meta WhatsApp API codes.
// Currently identity (en→en, it→it) because both systems use ISO 639-1.
// Extend this map if a language requires a different code on Meta's side (e.g. "pt"→"pt_BR").
func mapLanguageCode(lang string) string {
	langMap := map[string]string{
		"en": "en",
		"it": "it",
	}
	if mapped, ok := langMap[lang]; ok {
		return mapped
	}
	return lang
}

func maskPhone(phone string) string {
	if len(phone) <= 6 {
		return "***"
	}
	return phone[:3] + "***" + phone[len(phone)-4:]
}

// SendVerificationCode sends a verification code using a pre-approved template.
// Always sends with parameters — Meta ignores extra params for templates without variables.
func (c *WhatsAppClient) SendVerificationCode(ctx context.Context, toPhoneNumber, code, lang string) error {
	apiURL := fmt.Sprintf("%s/%s/messages", whatsAppAPIBaseURL, c.phoneNumberID)

	whatsappLangCode := mapLanguageCode(lang)

	template := map[string]interface{}{
		"name": c.templateName,
		"language": map[string]string{
			"code": whatsappLangCode,
		},
		"components": []map[string]interface{}{
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
		},
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

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	logger.Info.Printf("WhatsApp SendVerificationCode -> to:%s lang:%s template:%s", maskPhone(toPhoneNumber), lang, c.templateName)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var respObj map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&respObj)
		logger.Error.Printf("WhatsApp SendVerificationCode failed status=%d resp=%v", resp.StatusCode, respObj)
		return parseSendError(resp.StatusCode, respObj)
	}
	logger.Info.Println("WhatsApp SendVerificationCode: delivered to API")

	return nil
}

// SendTextMessage sends a simple text message
func (c *WhatsAppClient) SendTextMessage(ctx context.Context, toPhoneNumber, message string) error {
	apiURL := fmt.Sprintf("%s/%s/messages", whatsAppAPIBaseURL, c.phoneNumberID)

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

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	logger.Info.Printf("WhatsApp SendTextMessage -> to:%s", maskPhone(toPhoneNumber))
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

// SendTemplateMessage sends a message using a specific WhatsApp template with named parameters.
func (c *WhatsAppClient) SendTemplateMessage(ctx context.Context, toPhoneNumber, templateName, lang string, params map[string]string) error {
	apiURL := fmt.Sprintf("%s/%s/messages", whatsAppAPIBaseURL, c.phoneNumberID)

	whatsappLangCode := mapLanguageCode(lang)

	template := map[string]interface{}{
		"name": templateName,
		"language": map[string]string{
			"code": whatsappLangCode,
		},
	}

	if len(params) > 0 {
		var bodyParams []map[string]interface{}
		var components []map[string]interface{}

		for key, value := range params {
			if strings.HasPrefix(key, "button_") {
				btnIndex := strings.TrimPrefix(key, "button_")
				components = append(components, map[string]interface{}{
					"type":     "button",
					"sub_type": "url",
					"index":    btnIndex,
					"parameters": []map[string]interface{}{
						{"type": "text", "text": value},
					},
				})
			} else {
				bodyParams = append(bodyParams, map[string]interface{}{
					"type":           "text",
					"text":           value,
					"parameter_name": key,
				})
			}
		}

		if len(bodyParams) > 0 {
			components = append(components, map[string]interface{}{
				"type":       "body",
				"parameters": bodyParams,
			})
		}
		if len(components) > 0 {
			template["components"] = components
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

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	logger.Info.Printf("WhatsApp SendTemplateMessage -> to:%s template:%s lang:%s", maskPhone(toPhoneNumber), templateName, lang)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var respObj map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&respObj)
		logger.Error.Printf("WhatsApp SendTemplateMessage failed status=%d resp=%v", resp.StatusCode, respObj)
		return fmt.Errorf("failed to send template message, status code: %d", resp.StatusCode)
	}
	logger.Info.Println("WhatsApp SendTemplateMessage: delivered to API")

	return nil
}
