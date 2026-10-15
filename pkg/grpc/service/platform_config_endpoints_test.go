package service

import (
	"context"
	"testing"

	"github.com/influenzanet/user-management-service/internal/config"
	"github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetPlatformConfig is public: the participant site calls it before anyone logs in, to decide
// whether to show the phone and WhatsApp controls. It must answer from the same decision the
// phone endpoints use, and nothing else.

func TestGetPlatformConfig(t *testing.T) {
	on := &userManagementServer{whatsApp: config.NewWhatsAppSettings(config.WhatsAppConfig{Enabled: true})}
	off := &userManagementServer{whatsApp: config.NewWhatsAppSettings(config.WhatsAppConfig{Enabled: false})}

	t.Run("missing request or instance", func(t *testing.T) {
		for _, req := range []*api.PlatformConfigReq{nil, {}} {
			if _, err := on.GetPlatformConfig(context.Background(), req); status.Code(err) != codes.InvalidArgument {
				t.Errorf("GetPlatformConfig(%v) error = %v, want InvalidArgument", req, err)
			}
		}
	})

	t.Run("WhatsApp enabled", func(t *testing.T) {
		resp, err := on.GetPlatformConfig(context.Background(), &api.PlatformConfigReq{InstanceId: testInstanceID})
		if err != nil {
			t.Fatal(err)
		}
		if !resp.WhatsappEnabled {
			t.Error("WhatsappEnabled = false, want true")
		}
	})

	t.Run("WhatsApp disabled", func(t *testing.T) {
		resp, err := off.GetPlatformConfig(context.Background(), &api.PlatformConfigReq{InstanceId: testInstanceID})
		if err != nil {
			t.Fatal(err)
		}
		if resp.WhatsappEnabled {
			t.Error("WhatsappEnabled = true, want false")
		}
	})
}
