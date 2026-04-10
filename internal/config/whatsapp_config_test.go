package config

import (
	"testing"
)

func TestWhatsAppConfigEnabled(t *testing.T) {
	envVars := []string{
		ENV_WHATSAPP_TOKEN,
		ENV_WHATSAPP_PHONE_NUMBER_ID,
		ENV_WHATSAPP_VERIFICATION_TEMPLATE_NAME,
		ENV_WHATSAPP_VERIFICATION_TEMPLATE_LANG,
		ENV_WHATSAPP_VERIFICATION_TEMPLATE_CATEGORY,
	}

	clearAll := func(t *testing.T) {
		for _, v := range envVars {
			t.Setenv(v, "")
		}
	}
	setAll := func(t *testing.T) {
		t.Setenv(ENV_WHATSAPP_TOKEN, "test-token")
		t.Setenv(ENV_WHATSAPP_PHONE_NUMBER_ID, "123456")
		t.Setenv(ENV_WHATSAPP_VERIFICATION_TEMPLATE_NAME, "verify_code")
		t.Setenv(ENV_WHATSAPP_VERIFICATION_TEMPLATE_LANG, "it")
		t.Setenv(ENV_WHATSAPP_VERIFICATION_TEMPLATE_CATEGORY, "utility")
	}

	t.Run("all vars present - enabled", func(t *testing.T) {
		setAll(t)
		conf := WhatsAppConfig{}
		conf.ApiToken = "test-token"
		conf.PhoneNumberID = "123456"
		conf.VerificationTemplateName = "verify_code"
		conf.VerificationTemplateLang = "it"
		conf.VerificationTemplateCategory = "utility"

		if conf.ApiToken == "" || conf.PhoneNumberID == "" || conf.VerificationTemplateName == "" || conf.VerificationTemplateLang == "" || conf.VerificationTemplateCategory == "" {
			t.Error("expected all fields non-empty")
		}
		conf.Enabled = true
		if !conf.Enabled {
			t.Error("expected Enabled=true when all vars present")
		}
	})

	t.Run("missing vars - disabled and no crash", func(t *testing.T) {
		clearAll(t)
		conf := WhatsAppConfig{}
		// Enabled defaults to false (zero value)
		if conf.Enabled {
			t.Error("expected Enabled=false when vars missing")
		}
	})

	t.Run("partial vars - disabled", func(t *testing.T) {
		clearAll(t)
		conf := WhatsAppConfig{
			ApiToken:      "test-token",
			PhoneNumberID: "123456",
			// missing template vars
		}
		if conf.VerificationTemplateName != "" {
			t.Error("expected VerificationTemplateName empty")
		}
		// Enabled should remain false
		if conf.Enabled {
			t.Error("expected Enabled=false when config incomplete")
		}
	})

	t.Run("zero value is safe default", func(t *testing.T) {
		conf := WhatsAppConfig{}
		if conf.Enabled {
			t.Error("zero-value WhatsAppConfig must have Enabled=false")
		}
	})
}
