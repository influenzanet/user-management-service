package service

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	api_types "github.com/influenzanet/go-utils/pkg/api_types"
	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/api"
	"github.com/influenzanet/user-management-service/pkg/models"
	loggingMock "github.com/influenzanet/user-management-service/test/mocks/logging_service"
	messageMock "github.com/influenzanet/user-management-service/test/mocks/messaging_service"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestVerificationTemplateLang(t *testing.T) {
	cfg := func(langs ...string) config.WhatsAppConfig {
		return config.WhatsAppConfig{VerificationTemplateLang: "it", VerificationTemplateLangs: langs}
	}
	userIn := func(lang string) models.User {
		return models.User{Account: models.Account{PreferredLanguage: lang}}
	}
	for _, tc := range []struct {
		name string
		cfg  config.WhatsAppConfig
		user string
		want string
	}{
		{"user language available", cfg("it", "en"), "en", "en"},
		{"configured language itself", cfg("it", "en"), "it", "it"},
		{"user language not available falls back", cfg("it", "en"), "fr", "it"},
		{"romansh falls back", cfg("it", "en"), "rm", "it"},
		{"empty user language falls back", cfg("it", "en"), "", "it"},
		{"region code is normalised to Meta's form", cfg("it", "de_CH"), "de-CH", "de_CH"},
		{"region code not available falls back", cfg("it", "en"), "de-CH", "it"},
		{"match ignores case and sends the configured spelling", cfg("it", "en_US"), "en_us", "en_US"},
		{"upper-case user language matches", cfg("it", "en"), "EN", "en"},
		{"no list configured: only the configured language", cfg(), "en", "it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := verificationTemplateLang(tc.cfg, userIn(tc.user)); got != tc.want {
				t.Fatalf("verificationTemplateLang(user %q) = %q, want %q", tc.user, got, tc.want)
			}
		})
	}
}

// recordingWhatsAppClient captures the language handed over by each send.
type recordingWhatsAppClient struct {
	mu    sync.Mutex
	langs []string
}

func (c *recordingWhatsAppClient) SendVerificationCode(ctx context.Context, toPhoneNumber, code, lang string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.langs = append(c.langs, lang)
	return nil
}

func (c *recordingWhatsAppClient) SendTemplateMessage(ctx context.Context, toPhoneNumber, templateName, lang string, params map[string]string) error {
	return nil
}

func (c *recordingWhatsAppClient) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.langs...)
}

var languageRulePhoneSeq int64

func languageRulePhone() string {
	return fmt.Sprintf("+3933%07d", 4000000+atomic.AddInt64(&languageRulePhoneSeq, 1))
}

func languageRuleServer(t *testing.T, client WhatsAppClient, cfg config.WhatsAppConfig) userManagementServer {
	t.Helper()
	mockCtrl := gomock.NewController(t)
	t.Cleanup(mockCtrl.Finish)
	mockLoggingClient := loggingMock.NewMockLoggingServiceApiClient(mockCtrl)
	mockLoggingClient.EXPECT().SaveLogEvent(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	mockMessagingClient := messageMock.NewMockMessagingServiceApiClient(mockCtrl)
	mockMessagingClient.EXPECT().SendInstantEmail(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	cfg.Enabled = true
	return userManagementServer{
		userDBservice:     testUserDBService,
		globalDBService:   testGlobalDBService,
		instanceIDs:       []string{testInstanceID},
		Intervals:         models.Intervals{TokenExpiryInterval: time.Second * 2, VerificationCodeLifetime: 60},
		clients:           &models.APIClients{LoggingService: mockLoggingClient, MessagingService: mockMessagingClient},
		newUserCountLimit: 10000,
		whatsAppClient:    client,
		whatsAppConfig:    cfg,
	}
}

// languageRuleUser creates a confirmed user with an unverified phone, the state in which all three
// verification-code paths can send.
func languageRuleUser(t *testing.T, preferredLang, phone string) *api_types.TokenInfos {
	t.Helper()
	now := time.Now().Unix()
	users, err := addTestUsers([]models.User{{
		Account: models.Account{
			Type:                      "email",
			AccountID:                 fmt.Sprintf("lang-rule-%s@test.test", phone),
			AccountConfirmedAt:        now,
			PreferredLanguage:         preferredLang,
			PhoneVerificationAttempts: []int64{},
		},
		Profiles: []models.Profile{{ID: primitive.NewObjectID(), Alias: "main"}},
		ContactInfos: []models.ContactInfo{
			{ID: primitive.NewObjectID(), Type: models.ContactTypeEmail, Email: fmt.Sprintf("lang-rule-%s@test.test", phone), ConfirmedAt: now},
			{ID: primitive.NewObjectID(), Type: models.ContactTypePhone, Phone: phone, ConfirmedAt: 0},
		},
	}})
	if err != nil {
		t.Fatalf("failed to create test user: %s", err.Error())
	}
	return &api_types.TokenInfos{Id: users[0].ID.Hex(), InstanceId: testInstanceID}
}

// The three RPCs that send a verification code must hand the WhatsApp client the same language
// for the same user: the user's language when the template is available in it, the configured
// one otherwise.
func TestPhoneVerificationPathsShareOneLanguageRule(t *testing.T) {
	cfg := config.WhatsAppConfig{VerificationTemplateLang: "it", VerificationTemplateLangs: []string{"it", "en"}}
	for _, tc := range []struct{ userLang, want string }{
		{"it", "it"}, {"en", "en"}, {"fr", "it"}, {"de", "it"}, {"rm", "it"}, {"", "it"}, {"de-CH", "it"},
	} {
		t.Run("user="+tc.userLang, func(t *testing.T) {
			ctx := context.Background()
			paths := []struct {
				name string
				call func(s userManagementServer, token *api_types.TokenInfos, phone string) error
			}{
				{"AddPhoneNumber", func(s userManagementServer, token *api_types.TokenInfos, phone string) error {
					_, err := s.AddPhoneNumber(ctx, &api.PhoneMsg{Token: token, NewPhone: phone})
					return err
				}},
				{"EditPhoneNumber", func(s userManagementServer, token *api_types.TokenInfos, phone string) error {
					_, err := s.EditPhoneNumber(ctx, &api.PhoneMsg{Token: token, NewPhone: phone})
					return err
				}},
				{"ResendContactVerification", func(s userManagementServer, token *api_types.TokenInfos, phone string) error {
					_, err := s.ResendContactVerification(ctx, &api.ResendContactVerificationReq{Token: token, Type: models.ContactTypePhone, Address: phone})
					return err
				}},
			}
			for _, p := range paths {
				client := &recordingWhatsAppClient{}
				s := languageRuleServer(t, client, cfg)
				phone := languageRulePhone()
				token := languageRuleUser(t, tc.userLang, phone)
				if err := p.call(s, token, phone); err != nil {
					t.Fatalf("%s: unexpected error: %s", p.name, err.Error())
				}
				if got := client.recorded(); len(got) != 1 || got[0] != tc.want {
					t.Errorf("%s handed %v, want exactly one send in %q", p.name, got, tc.want)
				}
			}
		})
	}
}
