package models

import "time"

type DBConfig struct {
	URI             string
	DBNamePrefix    string
	Timeout         int
	NoCursorTimeout bool
	MaxPoolSize     uint64
	IdleConnTimeout int
}

// Intervals embeds configuration of time based parameters (durations, frequency, lifetime)
type Intervals struct {
	TokenExpiryInterval              time.Duration // interpreted in minutes later
	VerificationCodeLifetime         int64         // in seconds
	InvitationTokenLifetime          time.Duration // Duration of the invitation token lifetime
	ContactVerificationTokenLifetime time.Duration // Duration of the contact verification token lifetime
}
