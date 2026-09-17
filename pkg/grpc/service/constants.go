package service

const (
	contactVerificationMessageCooldown = 1 * 60 // Minimum delay between 2 verification code sending for a new contact, seconds
	loginVerificationCodeCooldown      = 20 // Minimum delay between 2 verification code sending for a new login, in seconds

	// Window time period to count event and limit rate
	signupRateLimitWindow              = 5 * 60  // to count the new signup, seconds
	loginFailedAttemptWindow           = 5 * 50  // to count the login failure, seconds
	phoneVerificationRateLimitWindow    = 5 * 60 // to count phone verification sends, seconds
	allowedPhoneVerificationAttempts    = 3       // sends allowed in the window, the next one (i.e. the 4th) is blocked
	// The budget above is per account, so it bounds what one account spends and nothing of what
	// one number receives: accounts are free to create. The two below are the same limit counted
	// per destination number, across every account, over a longer window because the abuse it
	// answers is somebody else's number being used as a target rather than a participant
	// mistyping their own.
	// The ceiling is deliberately well above the per-account one. A number is legitimately
	// reachable from more than one request — a participant retrying, changing their mind, coming
	// back the next day — and a tight per-number ceiling would lock them out of their own
	// verification for the whole window on somebody else's traffic. Ten an hour still makes
	// rotating accounts pointless while leaving honest use alone. Set on Orbyta's judgement,
	// pending confirmation from ISI.
	phoneDestinationRateLimitWindow = 60 * 60 // to count sends to one destination number, seconds
	allowedPhoneDestinationSends    = 10      // sends allowed per number in the window, the next one is blocked
	passwordResetAttemptWindow      = 60 * 60 // to count the password failure, in seconds, default=1 hour
	allowedPasswordAttempts         = 10
	allowedVerificationCodeAttempts = 3

	userCreationTimestampOffset = 7 * 24 * 3600 // consider user deletion only after this time, when created by admin

	maximumProfilesAllowed = 6
)
