package interceptors

import (
	"context"
	"testing"

	"github.com/influenzanet/user-management-service/pkg/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The public GetPlatformConfig request carries an instance id and no token, so the interceptor
// is what keeps it to the instances this service knows.
func TestInstanceIdInterceptorGuardsPlatformConfig(t *testing.T) {
	intercept := InstanceIdInterceptor([]string{"italy"})
	handler := func(ctx context.Context, req interface{}) (interface{}, error) { return "reached", nil }

	for _, tc := range []struct {
		instance string
		want     codes.Code
	}{
		{"italy", codes.OK},
		{"default", codes.InvalidArgument},
		{"", codes.InvalidArgument},
	} {
		_, err := intercept(context.Background(), &api.PlatformConfigReq{InstanceId: tc.instance}, &grpc.UnaryServerInfo{}, handler)
		if status.Code(err) != tc.want {
			t.Errorf("instance %q: code = %v, want %v", tc.instance, status.Code(err), tc.want)
		}
	}
}
