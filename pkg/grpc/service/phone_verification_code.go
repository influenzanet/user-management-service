package service

import (
	"time"

	"github.com/coneno/logger"
	"github.com/influenzanet/user-management-service/pkg/models"
	"github.com/influenzanet/user-management-service/pkg/tokens"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newPhoneVerificationCode draws a code for the number and returns it, for the message, together
// with what the account stores for it: the code's digest and never the code itself. caller names
// the endpoint in the log.
func (s *userManagementServer) newPhoneVerificationCode(caller, userID, phone string) (string, models.PhoneVerificationCode, error) {
	vc, err := tokens.GenerateVerificationCode(6)
	if err != nil {
		logger.Error.Printf("%s: failed to generate verification code: %v", caller, err)
		return "", models.PhoneVerificationCode{}, status.Error(codes.Internal, "error while generating verification code")
	}
	digest, err := tokens.PhoneCodeDigest(userID, vc)
	if err != nil {
		logger.Error.Printf("%s: failed to digest verification code: %v", caller, err)
		return "", models.PhoneVerificationCode{}, status.Error(codes.Internal, "error while generating verification code")
	}
	now := time.Now().Unix()
	return vc, models.PhoneVerificationCode{
		CodeHash:  digest,
		Attempts:  0,
		CreatedAt: now,
		ExpiresAt: now + s.Intervals.VerificationCodeLifetime,
		Phone:     phone,
	}, nil
}
