package config

import "time"

const (
	ENV_VERIFICATION_CODE_LIFETIME          = "VERIFICATION_CODE_LIFETIME"
	ENV_TOKEN_EXPIRATION_MIN                = "TOKEN_EXPIRATION_MIN"
	ENV_TOKEN_INVITATION_LIFETIME           = "INVITATION_TOKEN_LIFETIME"
	ENV_TOKEN_CONTACT_VERIFICATION_LIFETIME = "CONTACT_VERIFICATION_TOKEN_LIFETIME"

	ENV_USE_NO_CURSOR_TIMEOUT                   = "USE_NO_CURSOR_TIMEOUT"
	ENV_SEND_REMINDER_TO_UNVERIFIED_USERS_AFTER = "SEND_REMINDER_TO_UNVERIFIED_USERS_AFTER"
	ENV_NOTIFY_INACTIVE_USERS_AFTER             = "NOTIFY_INACTIVE_USERS_AFTER"
	ENV_DELETE_ACCOUNT_AFTER_NOTIFYING_USER     = "DELETE_ACCOUNT_AFTER_NOTIFYING_USER"

	ENV_WEEKDAY_ASSIGNATION_WEIGHTS = "WEEKDAY_ASSIGNATION_WEIGHTS"
)

const (
	defaultVerificationCodeLifetime         = 15 * 60 // for 2FA 6 digit code
	defaultTokenExpirationMin               = 55
	defaultInvitationTokenLifetime          = time.Hour * 24 * 7
	defaultContactVerificationTokenLifetime = time.Hour * 24 * 30
	defaultNotifyInactiveUsersAfter         = 0
	defaultDeleteAccountAfterNotifyingUser  = 0
)
