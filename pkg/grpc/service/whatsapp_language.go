package service

import (
	"strings"

	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/models"
)

// verificationTemplateLang is the one rule for the language of a phone verification code, shared
// by AddPhoneNumber, EditPhoneNumber and ResendContactVerification: the user's own language when
// the verification template is available in it, the configured language otherwise. The match
// ignores case and the hyphen/underscore difference, and the value sent is the configured entry,
// in Meta's form ("de_CH"), never the user's spelling.
func verificationTemplateLang(cfg config.WhatsAppConfig, user models.User) string {
	preferred := strings.ReplaceAll(strings.TrimSpace(user.Account.PreferredLanguage), "-", "_")
	for _, available := range cfg.VerificationTemplateLangs {
		if preferred != "" && strings.EqualFold(preferred, available) {
			return available
		}
	}
	return cfg.VerificationTemplateLang
}
