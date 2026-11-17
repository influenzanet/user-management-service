package service

import "github.com/influenzanet/user-management-service/pkg/dbs/userdb"

const (
	contactVerificationMessageCooldown = 1 * 60 // Minimum delay between 2 verification code sending for a new contact, seconds
	loginVerificationCodeCooldown      = 20     // Minimum delay between 2 verification code sending for a new login, in seconds

	// Window time period to count event and limit rate
	signupRateLimitWindow            = 5 * 60 // to count the new signup, seconds
	loginFailedAttemptWindow         = 5 * 50 // to count the login failure, seconds
	phoneVerificationRateLimitWindow = 5 * 60 // to count phone verification sends, seconds
	allowedPhoneVerificationAttempts = 3      // sends allowed in the window, the next one (i.e. the 4th) is blocked
	// The budget above is per account, so it bounds what one account spends and nothing of what
	// one number receives: accounts are free to create. The limits below are counted per
	// destination number, across every account, because the abuse they answer is somebody else's
	// number being used as a target rather than a participant mistyping their own.
	// The hourly ceiling is deliberately well above the per-account one. A number is legitimately
	// reachable from more than one request — a participant retrying, changing their mind, coming
	// back later — and a tight per-number ceiling would lock them out of their own verification on
	// somebody else's traffic. The daily ceiling stops the hourly one from being spent again every
	// hour: at 10 an hour alone, rotating accounts can still send one number 240 paid codes a day,
	// and every one of them risks a block or report against the sending number the reminders use.
	phoneDestinationRateLimitWindow   = 60 * 60      // to count sends to one destination number, seconds
	allowedPhoneDestinationSends      = 10           // sends allowed per number in the window, the next one is blocked
	phoneDestinationDailyWindow       = 24 * 60 * 60 // to count the daily sends to one destination number, seconds
	allowedPhoneDestinationSendsDaily = 20           // sends allowed per number in a day, the next one is blocked
	passwordResetAttemptWindow        = 60 * 60      // to count the password failure, in seconds, default=1 hour
	allowedPasswordAttempts           = 10
	allowedVerificationCodeAttempts   = 3

	userCreationTimestampOffset = 7 * 24 * 3600 // consider user deletion only after this time, when created by admin

	maximumProfilesAllowed = 6
)

// phoneDestinationSendLimits are the limits every verification send to a number is reserved
// against, all in one reservation (see ReservePhoneDestinationSendSlot).
var phoneDestinationSendLimits = []userdb.SendLimit{
	{MaxSends: allowedPhoneDestinationSends, WindowSeconds: phoneDestinationRateLimitWindow},
	{MaxSends: allowedPhoneDestinationSendsDaily, WindowSeconds: phoneDestinationDailyWindow},
}
