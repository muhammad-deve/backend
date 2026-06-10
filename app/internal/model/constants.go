package model

import "time"

const (
	// OTPCodeLength is the number of digits in a generated OTP code.
	OTPCodeLength = 6
	// OTPCodeAlphabet is the set of characters used to build an OTP code.
	OTPCodeAlphabet = "1234567890"
	// OTPExpiry is how long an issued OTP stays valid.
	OTPExpiry = 5 * time.Minute

	// MinPasswordLength is the minimum number of characters in a password.
	MinPasswordLength = 8

	AmoCRMGrantTypeAuthorizationCode = "authorization_code"
	AmoCRMGrantTypeRefreshToken      = "refresh_token"
)

// ValidatePassword enforces the password policy: at least MinPasswordLength
// characters, including at least one letter and one digit. Returns a
// human-readable reason when invalid.
func ValidatePassword(pw string) (bool, string) {
	if len(pw) < MinPasswordLength {
		return false, "Password must be at least 8 characters"
	}
	var hasLetter, hasDigit bool
	for _, r := range pw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if !hasLetter {
		return false, "Password must contain at least one letter"
	}
	if !hasDigit {
		return false, "Password must contain at least one number"
	}
	return true, ""
}
