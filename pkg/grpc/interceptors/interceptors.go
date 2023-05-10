package interceptors

import (
	"context"
	"reflect"

	"github.com/coneno/logger"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func InstanceIdInterceptor(instanceIDs []string) func(ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler) (interface{}, error) {

	return func(ctx context.Context,
		req interface{},
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (interface{}, error) {

		reqValue := reflect.ValueOf(req).Elem()
		instanceId := reqValue.FieldByName("InstanceId")
		token := reqValue.FieldByName("Token")

		allowed := true

		if !token.IsValid() && instanceId.IsValid() {
			allowed = false
			for _, allowedId := range instanceIDs {
				if instanceId.String() == allowedId {
					allowed = true
					break
				}
			}
		}

		if !allowed {
			logger.Warning.Printf("instance ID not allowed: %s", instanceId.String())
			return nil, status.Error(codes.InvalidArgument, "invalid arguments")
		}

		h, err := handler(ctx, req)
		return h, err
	}
}
