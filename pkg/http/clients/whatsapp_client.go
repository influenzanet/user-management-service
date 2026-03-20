package clients

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/coneno/logger"
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

func mapLanguageCode(lang string) string {

	langMap := map[string]string{
		"en": "en",
		"it": "it",
	}

	// Return mapped code if exists, otherwise return original
	if mapped, ok := langMap[lang]; ok {
		return mapped
	}
	return lang
}

// SendVerificationCode sends a verification code using a pre-approved template
func (c *WhatsAppClient) SendVerificationCode(toPhoneNumber, code, lang string) error {
	apiURL := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/messages", c.phoneNumberID)

	// Map language codes to WhatsApp template language codes
	// Default to the input lang if no mapping exists
	whatsappLangCode := mapLanguageCode(lang)

	// Base template structure
	// Builds the base template
	template := map[string]interface{}{
		"name": c.templateName,
		"language": map[string]string{
			"code": whatsappLangCode,
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

// SendTemplateMessage sends a message using a specific WhatsApp template with named parameters.
// Named parameters (parameter_name) are required by Meta Cloud API v19.0+ for templates
// that use named variable syntax (e.g. {{study_key}}) instead of positional ({{1}}, {{2}}).
func (c *WhatsAppClient) SendTemplateMessage(toPhoneNumber, templateName, lang string, params map[string]string) error {
	apiURL := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/messages", c.phoneNumberID)

	whatsappLangCode := mapLanguageCode(lang)

	// Build template structure
	template := map[string]interface{}{
		"name": templateName,
		"language": map[string]string{
			"code": whatsappLangCode,
		},
	}

	// Add parameters if provided. Parameters whose key starts with "button_<index>"
	// are sent as button URL components; all others are sent as body components.
	if len(params) > 0 {
		var bodyParams []map[string]interface{}
		var components []map[string]interface{}

		for key, value := range params {
			if strings.HasPrefix(key, "button_") {
				// Extract button index from key, e.g. "button_0" → index "0"
				btnIndex := strings.TrimPrefix(key, "button_")
				components = append(components, map[string]interface{}{
					"type":     "button",
					"sub_type": "url",
					"index":    btnIndex,
					"parameters": []map[string]interface{}{
						{
							"type": "text",
							"text": value,
						},
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

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	logger.Info.Printf("WhatsApp SendTemplateMessage -> to:%s template:%s lang:%s", toPhoneNumber, templateName, lang)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var respObj map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&respObj)
		logger.Error.Printf("WhatsApp SendTemplateMessage failed status=%d resp=%v", resp.StatusCode, respObj)
		logger.Error.Printf("WhatsApp Request payload was: %s", string(body))
		return fmt.Errorf("failed to send template message, status code: %d", resp.StatusCode)
	}
	logger.Info.Println("WhatsApp SendTemplateMessage: delivered to API")

	return nil
}
