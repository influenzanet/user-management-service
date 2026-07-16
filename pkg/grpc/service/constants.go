package service

const (
	contactVerificationMessageCooldown = 1 * 60 // Minimum delay between 2 verification code sending for a new contact, seconds
	loginVerificationCodeCooldown      = 20 // Minimum delay between 2 verification code sending for a new login, in seconds

	// Window time period to count event and limit rate
	signupRateLimitWindow              = 5 * 60  // to count the new signup, seconds
	loginFailedAttemptWindow           = 5 * 50  // to count the login failure, seconds
	phoneVerificationRateLimitWindow    = 5 * 60 // to count phone verification sends, seconds
	allowedPhoneVerificationAttempts    = 3       // sends allowed in the window, the next one (i.e. the 4th) is blocked
	passwordResetAttemptWindow      = 60 * 60 // to count the password failure, in seconds, default=1 hour
	allowedPasswordAttempts         = 10
	allowedVerificationCodeAttempts = 3

	userCreationTimestampOffset = 7 * 24 * 3600 // consider user deletion only after this time, when created by admin

	maximumProfilesAllowed = 6
)
