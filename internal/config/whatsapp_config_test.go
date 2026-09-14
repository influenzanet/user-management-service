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

func TestWhatsAppAPIVersionIsReadFromEnv(t *testing.T) {
	// InitConfig exits on missing DB credentials and counters, so give it the minimum it needs.
	for name, value := range map[string]string{
		"USER_DB_CONNECTION_STR": "localhost:27017", "USER_DB_USERNAME": "u", "USER_DB_PASSWORD": "p",
		"GLOBAL_DB_CONNECTION_STR": "localhost:27017", "GLOBAL_DB_USERNAME": "u", "GLOBAL_DB_PASSWORD": "p",
		"DB_TIMEOUT": "30", "DB_IDLE_CONN_TIMEOUT": "45", "DB_MAX_POOL_SIZE": "8",
		ENV_NEW_USER_RATE_LIMIT: "100", ENV_CLEAN_UP_UNVERIFIED_USERS_AFTER: "1",
		ENV_SEND_REMINDER_TO_UNVERIFIED_USERS_AFTER: "1",
		ENV_WEEKDAY_ASSIGNATION_WEIGHTS:             "",
	} {
		t.Setenv(name, value)
	}
	t.Setenv(ENV_WHATSAPP_API_VERSION, "v25.0")
	conf := InitConfig()
	if conf.WhatsApp.ApiVersion != "v25.0" {
		t.Fatalf("WhatsApp.ApiVersion = %q, want the value of %s", conf.WhatsApp.ApiVersion, ENV_WHATSAPP_API_VERSION)
	}
}
