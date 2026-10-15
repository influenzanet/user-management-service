package config

import (
	"fmt"
	"strings"
)

// WhatsAppSettings is the one place where user-management decides whether WhatsApp is on for
// an instance, and with which settings. Every check takes the instance it works on. Today all
// instances share the configuration read from the environment at start-up, so the instance is
// not read yet: when the configuration moves to the database, only this type changes.
type WhatsAppSettings struct {
	conf WhatsAppConfig
}

// NewWhatsAppSettings serves conf, as decided by InitConfig, to every instance.
func NewWhatsAppSettings(conf WhatsAppConfig) WhatsAppSettings {
	return WhatsAppSettings{conf: conf}
}

// Enabled reports whether WhatsApp is on for the instance: the switch is "true" and the
// configuration needed to send the verification template is complete (see whatsAppEnabled).
func (s WhatsAppSettings) Enabled(instanceID string) bool {
	return s.conf.Enabled
}

// ForInstance returns the WhatsApp settings of the instance.
func (s WhatsAppSettings) ForInstance(instanceID string) WhatsAppConfig {
	return s.conf
}

// whatsAppProblems lists what is incomplete or incoherent in the WhatsApp configuration, one
// line per problem, naming the variables and never printing a credential.
func whatsAppProblems(flag string, conf WhatsAppConfig) []string {
	problems := []string{}
	if flag == "true" {
		missing := []string{}
		for _, v := range []struct{ name, value string }{
			{ENV_WHATSAPP_TOKEN, conf.ApiToken},
			{ENV_WHATSAPP_PHONE_NUMBER_ID, conf.PhoneNumberID},
			{ENV_WHATSAPP_VERIFICATION_TEMPLATE_NAME, conf.VerificationTemplateName},
			{ENV_WHATSAPP_VERIFICATION_TEMPLATE_LANG, conf.VerificationTemplateLang},
			{ENV_WHATSAPP_VERIFICATION_TEMPLATE_CATEGORY, conf.VerificationTemplateCategory},
		} {
			if v.value == "" {
				missing = append(missing, v.name)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("WhatsApp disabled: %s is \"true\" but the configuration is incomplete, missing: %s",
				ENV_WHATSAPP_ENABLED, strings.Join(missing, ", ")))
		}
		return problems
	}
	if flag != "" && flag != "false" {
		problems = append(problems, fmt.Sprintf("WhatsApp disabled: %s is %q, only the exact value \"true\" enables it", ENV_WHATSAPP_ENABLED, flag))
	}
	if conf.ApiToken != "" || conf.PhoneNumberID != "" {
		problems = append(problems, fmt.Sprintf("WhatsApp credentials are configured but ignored because %s is not \"true\"", ENV_WHATSAPP_ENABLED))
	}
	return problems
}
