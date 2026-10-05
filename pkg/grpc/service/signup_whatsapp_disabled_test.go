package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/models"
)

// With WhatsApp switched off for the platform a phone number has no use: it can be neither
// verified nor messaged. Signup must therefore not store the number the client sends, must not
// let it decide the outcome, and must not write it to the log.

// captureInfo collects what the service writes to the info log while fn runs.
func captureInfo(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	original := logger.Info.Writer()
	logger.Info.SetOutput(&buf)
	defer logger.Info.SetOutput(original)
	fn()
	return buf.String()
}

func TestSignupWithEmail_PhoneIgnoredWhenWhatsAppDisabled(t *testing.T) {
	s := newSignupTestServer(t)
	s.whatsAppConfig.Enabled = false

	for _, tc := range []struct {
		name  string
		email string
		phone string
	}{
		{"valid number", "wa_off_valid@test.com", "+391230002001"},
		{"malformed number", "wa_off_malformed@test.com", "not-a-number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var signupErr error
			logged := captureInfo(t, func() {
				logged := captureWarnings(t, func() {
					_, signupErr = s.SignupWithEmail(context.Background(), signupReq(tc.email, tc.phone))
				})
				if strings.Contains(logged, tc.phone) || strings.Contains(logged, "2001") {
					t.Errorf("the warning log carries the ignored number: %q", logged)
				}
			})
			if signupErr != nil {
				t.Fatalf("signup failed because of a number it should have ignored: %s", signupErr.Error())
			}
			if strings.Contains(logged, tc.phone) || strings.Contains(logged, "2001") {
				t.Errorf("the info log carries the ignored number: %q", logged)
			}
			if !strings.Contains(logged, "WhatsApp is disabled") {
				t.Errorf("no log line says the number was ignored because WhatsApp is disabled: %q", logged)
			}

			user, err := testUserDBService.GetUserByAccountID(testInstanceID, tc.email)
			if err != nil {
				t.Fatalf("unexpected error: %s", err.Error())
			}
			for _, ci := range user.ContactInfos {
				if ci.Type == models.ContactTypePhone {
					t.Errorf("a phone contact was stored although WhatsApp is disabled: %+v", ci)
				}
			}
		})
	}
}
