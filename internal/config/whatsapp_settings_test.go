package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/coneno/logger"
)

func completeWhatsAppConfig() WhatsAppConfig {
	return WhatsAppConfig{
		ApiToken:                     "secret-wa-token",
		PhoneNumberID:                "secret-phone-id",
		VerificationTemplateName:     "verify_code",
		VerificationTemplateLang:     "it",
		VerificationTemplateCategory: "utility",
	}
}

func TestWhatsAppSettingsIsTheSingleDecision(t *testing.T) {
	on := completeWhatsAppConfig()
	on.Enabled = true
	off := completeWhatsAppConfig()

	for _, instanceID := range []string{"italy", "default", ""} {
		if !NewWhatsAppSettings(on).Enabled(instanceID) {
			t.Errorf("Enabled(%q) = false for an enabled configuration", instanceID)
		}
		if NewWhatsAppSettings(off).Enabled(instanceID) {
			t.Errorf("Enabled(%q) = true for a disabled configuration", instanceID)
		}
		if got := NewWhatsAppSettings(on).ForInstance(instanceID); got.VerificationTemplateName != on.VerificationTemplateName {
			t.Errorf("ForInstance(%q) does not return the configuration: %+v", instanceID, got)
		}
	}
	if (WhatsAppSettings{}).Enabled("italy") {
		t.Error("the zero WhatsAppSettings must be disabled")
	}
}

func TestWhatsAppProblems(t *testing.T) {
	complete := completeWhatsAppConfig()
	withoutTokenAndCategory := complete
	withoutTokenAndCategory.ApiToken = ""
	withoutTokenAndCategory.VerificationTemplateCategory = ""

	for _, tc := range []struct {
		name       string
		flag       string
		conf       WhatsAppConfig
		wantNamed  []string
		wantAbsent []string
		wantNone   bool
	}{
		{name: "switch on, complete", flag: "true", conf: complete, wantNone: true},
		{name: "switch off, nothing configured", flag: "", conf: WhatsAppConfig{}, wantNone: true},
		{name: "switch false, nothing configured", flag: "false", conf: WhatsAppConfig{}, wantNone: true},
		{
			name: "switch on, token and category missing", flag: "true", conf: withoutTokenAndCategory,
			wantNamed:  []string{ENV_WHATSAPP_TOKEN, ENV_WHATSAPP_VERIFICATION_TEMPLATE_CATEGORY},
			wantAbsent: []string{ENV_WHATSAPP_PHONE_NUMBER_ID, ENV_WHATSAPP_VERIFICATION_TEMPLATE_NAME},
		},
		{
			name: "switch on, nothing configured", flag: "true", conf: WhatsAppConfig{},
			wantNamed: []string{ENV_WHATSAPP_TOKEN, ENV_WHATSAPP_PHONE_NUMBER_ID, ENV_WHATSAPP_VERIFICATION_TEMPLATE_NAME,
				ENV_WHATSAPP_VERIFICATION_TEMPLATE_LANG, ENV_WHATSAPP_VERIFICATION_TEMPLATE_CATEGORY},
		},
		{name: "switch with other casing", flag: "TRUE", conf: complete, wantNamed: []string{ENV_WHATSAPP_ENABLED, `"TRUE"`}},
		{name: "switch off, credentials left", flag: "false", conf: complete, wantNamed: []string{ENV_WHATSAPP_ENABLED}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := whatsAppProblems(tc.flag, tc.conf)
			if tc.wantNone {
				if len(problems) != 0 {
					t.Fatalf("whatsAppProblems = %q, want none", problems)
				}
				return
			}
			if len(problems) == 0 {
				t.Fatal("whatsAppProblems found nothing")
			}
			all := strings.Join(problems, "\n")
			for _, name := range tc.wantNamed {
				if !strings.Contains(all, name) {
					t.Errorf("problems do not name %s: %q", name, all)
				}
			}
			for _, name := range tc.wantAbsent {
				if strings.Contains(all, name) {
					t.Errorf("problems name %s, which is set: %q", name, all)
				}
			}
			for _, secret := range []string{"secret-wa-token", "secret-phone-id"} {
				if strings.Contains(all, secret) {
					t.Errorf("problems leak a credential value: %q", all)
				}
			}
		})
	}
}

// captureWarning collects what InitConfig writes to the warning log while fn runs.
func captureWarning(fn func()) string {
	var buf bytes.Buffer
	original := logger.Warning.Writer()
	logger.Warning.SetOutput(&buf)
	defer logger.Warning.SetOutput(original)
	fn()
	return buf.String()
}

func TestInitConfigWarnsWithTheMissingVariablesOnly(t *testing.T) {
	initConfigEnv(t)
	t.Setenv(ENV_WHATSAPP_ENABLED, "true")
	t.Setenv(ENV_WHATSAPP_TOKEN, "")

	var conf Config
	out := captureWarning(func() { conf = InitConfig() })
	if conf.WhatsApp.Enabled {
		t.Fatal("WhatsApp enabled without a token")
	}
	if !strings.Contains(out, ENV_WHATSAPP_TOKEN) {
		t.Errorf("the warning does not name %s: %q", ENV_WHATSAPP_TOKEN, out)
	}
	if strings.Contains(out, ENV_WHATSAPP_PHONE_NUMBER_ID) {
		t.Errorf("the warning names %s, which is set: %q", ENV_WHATSAPP_PHONE_NUMBER_ID, out)
	}
	if strings.Contains(out, "123456") {
		t.Errorf("the warning leaks the phone number id: %q", out)
	}
}
