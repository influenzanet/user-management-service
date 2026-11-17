package config

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/coneno/logger"
)

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

func TestVerificationTemplateLangsAlwaysContainTheConfiguredLanguage(t *testing.T) {
	for _, tc := range []struct {
		list, defaultLang string
		want              []string
	}{
		{"", "it", []string{"it"}},
		{"it, en", "it", []string{"it", "en"}},
		{"en", "it", []string{"en", "it"}},
		{" it ,, en ,", "it", []string{"it", "en"}},
		{"it, de-CH", "it", []string{"it", "de_CH"}},
		{"IT, en_US", "it", []string{"IT", "en_US"}},
	} {
		got := verificationTemplateLangs(tc.list, tc.defaultLang)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("verificationTemplateLangs(%q, %q) = %v, want %v", tc.list, tc.defaultLang, got, tc.want)
		}
	}
}

// initConfigEnv gives InitConfig the minimum it needs to return, plus a complete WhatsApp
// configuration, so that only WHATSAPP_ENABLED decides the outcome.
func initConfigEnv(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"USER_DB_CONNECTION_STR": "localhost:27017", "USER_DB_USERNAME": "u", "USER_DB_PASSWORD": "p",
		"GLOBAL_DB_CONNECTION_STR": "localhost:27017", "GLOBAL_DB_USERNAME": "u", "GLOBAL_DB_PASSWORD": "p",
		"DB_TIMEOUT": "30", "DB_IDLE_CONN_TIMEOUT": "45", "DB_MAX_POOL_SIZE": "8",
		ENV_NEW_USER_RATE_LIMIT: "100", ENV_CLEAN_UP_UNVERIFIED_USERS_AFTER: "1",
		ENV_SEND_REMINDER_TO_UNVERIFIED_USERS_AFTER: "1",
		ENV_WEEKDAY_ASSIGNATION_WEIGHTS:             "",
		ENV_WHATSAPP_TOKEN:                          "secret-wa-token",
		ENV_WHATSAPP_PHONE_NUMBER_ID:                "123456",
		ENV_WHATSAPP_VERIFICATION_TEMPLATE_NAME:     "verify_code",
		ENV_WHATSAPP_VERIFICATION_TEMPLATE_LANG:     "it",
	} {
		t.Setenv(name, value)
	}
}

// captureInfo collects what InitConfig writes to the info log while fn runs.
func captureInfo(fn func()) string {
	var buf bytes.Buffer
	original := logger.Info.Writer()
	logger.Info.SetOutput(&buf)
	defer logger.Info.SetOutput(original)
	fn()
	return buf.String()
}

func TestInitConfigWhatsAppNeedsTheEnabledSwitch(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want bool
	}{
		{"", false},
		{"false", false},
		{"TRUE", false},
		{"1", false},
		{"true", true},
	} {
		t.Run("WHATSAPP_ENABLED="+tc.flag, func(t *testing.T) {
			initConfigEnv(t)
			t.Setenv(ENV_WHATSAPP_ENABLED, tc.flag)
			var conf Config
			out := captureInfo(func() { conf = InitConfig() })
			if conf.WhatsApp.Enabled != tc.want {
				t.Fatalf("WhatsApp.Enabled = %v with %s=%q and a complete configuration, want %v", conf.WhatsApp.Enabled, ENV_WHATSAPP_ENABLED, tc.flag, tc.want)
			}
			if !tc.want && !strings.Contains(out, ENV_WHATSAPP_ENABLED) {
				t.Errorf("the info log does not say WhatsApp is disabled by %s: %q", ENV_WHATSAPP_ENABLED, out)
			}
			if strings.Contains(out, "secret-wa-token") {
				t.Errorf("the info log leaks the WhatsApp token: %q", out)
			}
		})
	}
}

func TestWhatsAppEnabledDecision(t *testing.T) {
	complete := WhatsAppConfig{
		ApiToken:                 "test-token",
		PhoneNumberID:            "123456",
		VerificationTemplateName: "verify_code",
		VerificationTemplateLang: "it",
	}
	withoutToken := complete
	withoutToken.ApiToken = ""
	withoutPhoneNumberID := complete
	withoutPhoneNumberID.PhoneNumberID = ""
	withoutTemplateName := complete
	withoutTemplateName.VerificationTemplateName = ""
	withoutTemplateLang := complete
	withoutTemplateLang.VerificationTemplateLang = ""

	for _, tc := range []struct {
		name string
		flag string
		conf WhatsAppConfig
		want bool
	}{
		{"switch on, complete configuration", "true", complete, true},
		{"switch unset, complete configuration", "", complete, false},
		{"switch false, complete configuration", "false", complete, false},
		{"switch with other casing", "True", complete, false},
		{"switch with spaces", " true", complete, false},
		{"switch on, token missing", "true", withoutToken, false},
		{"switch on, phone number id missing", "true", withoutPhoneNumberID, false},
		{"switch on, template name missing", "true", withoutTemplateName, false},
		{"switch on, template language missing", "true", withoutTemplateLang, false},
		{"switch on, empty configuration", "true", WhatsAppConfig{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := whatsAppEnabled(tc.flag, tc.conf); got != tc.want {
				t.Errorf("whatsAppEnabled(%q, ...) = %v, want %v", tc.flag, got, tc.want)
			}
		})
	}
}

// The verification template's category is not configuration: Meta's send call takes none, and
// nothing in the service reads it. A deployment that never sets the old variable gets WhatsApp,
// and one that still sets it is neither refused nor warned.
func TestInitConfigWhatsAppNeedsNoTemplateCategory(t *testing.T) {
	for _, category := range []string{"", "AUTHENTICATION"} {
		t.Run("WHATSAPP_VERIFICATION_TEMPLATE_CATEGORY="+category, func(t *testing.T) {
			initConfigEnv(t)
			t.Setenv(ENV_WHATSAPP_ENABLED, "true")
			t.Setenv("WHATSAPP_VERIFICATION_TEMPLATE_CATEGORY", category)
			var conf Config
			out := captureWarning(func() { conf = InitConfig() })
			if !conf.WhatsApp.Enabled {
				t.Fatalf("WhatsApp disabled with the switch on and every required variable set; warnings: %q", out)
			}
			if strings.Contains(out, "WhatsApp") {
				t.Errorf("InitConfig warns about WhatsApp with a complete configuration: %q", out)
			}
		})
	}
}
