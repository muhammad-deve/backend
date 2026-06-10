package model

import "time"

const (
	// OTPCodeLength is the number of digits in a generated OTP code.
	OTPCodeLength = 6
	// OTPCodeAlphabet is the set of characters used to build an OTP code.
	OTPCodeAlphabet = "1234567890"
	// OTPExpiry is how long an issued OTP stays valid.
	OTPExpiry = 5 * time.Minute

	AmoCRMGrantTypeAuthorizationCode = "authorization_code"
	AmoCRMGrantTypeRefreshToken      = "refresh_token"
)
