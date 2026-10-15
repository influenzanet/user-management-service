package service

import (
	"context"

	"github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetPlatformConfig tells the participant site which optional features are on for an
// instance, before anyone logs in. It answers from the same decision the phone endpoints use,
// so the site shows the phone and WhatsApp controls exactly when the backend accepts them.
// The request carries no token: the instance id interceptor limits it to known instances.
func (s *userManagementServer) GetPlatformConfig(ctx context.Context, req *api.PlatformConfigReq) (*api.PlatformConfig, error) {
	if req == nil || req.InstanceId == "" {
		return nil, status.Error(codes.InvalidArgument, "missing argument")
	}
	return &api.PlatformConfig{WhatsappEnabled: s.whatsApp.Enabled(req.InstanceId)}, nil
}
