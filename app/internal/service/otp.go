package service

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// errRateLimited is returned internally when a send is throttled. The handler
// maps it to HTTP 429 while keeping the public response enumeration-safe.
type errRateLimited struct {
	retryAfter time.Duration
}

func (e *errRateLimited) Error() string {
	return fmt.Sprintf("too many requests, retry after %s", e.retryAfter.Round(time.Second))
}

// RetryAfterSeconds returns the suggested wait time in whole seconds.
func (e *errRateLimited) RetryAfterSeconds() int {
	secs := int(e.retryAfter.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs
}

// IsRateLimited reports whether err is a rate-limit error and returns it.
func IsRateLimited(err error) (*errRateLimited, bool) {
	var rl *errRateLimited
	if errors.As(err, &rl) {
		return rl, true
	}
	return nil, false
}

type OTPI interface {
	SendOTP(req *model.SendOTPRequest) (*model.SendOTPResponse, error)
	VerifyOTP(req *model.VerifyOTPRequest) (*model.VerifyOTPResponse, error)
}

type otpService struct {
	app     *pocketbase.PocketBase
	email   EmailI
	limiter *otpRateLimiter
}

func NewOTPService(app *pocketbase.PocketBase, email EmailI) OTPI {
	return &otpService{
		app:   app,
		email: email,
		// Max 5 codes per email per hour, with a 60s cooldown between sends.
		limiter: newOTPRateLimiter(time.Hour, 5, 60*time.Second),
	}
}

// SendOTP finds-or-creates the user by email, issues a native PocketBase OTP
// linked to that user, emails the code, and returns the OTP id.
func (s *otpService) SendOTP(req *model.SendOTPRequest) (*model.SendOTPResponse, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("a valid email is required")
	}
	name := strings.TrimSpace(req.Name)

	// Throttle before doing any work so abuse can't drive user creation or
	// email sends. Keyed by email; resets on restart.
	if allowed, retryAfter := s.limiter.Allow(email); !allowed {
		return nil, &errRateLimited{retryAfter: retryAfter}
	}

	usersCollection, err := s.app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		return nil, fmt.Errorf("users collection not found: %w", err)
	}

	// Find or create the auth record. The native _otps collection requires
	// recordRef to point at an existing auth record, so the user must exist
	// before an OTP can be stored.
	user, err := s.app.FindAuthRecordByEmail(usersCollection, email)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("failed to lookup user: %w", err)
		}
		user = core.NewRecord(usersCollection)
		user.SetEmail(email)
		if name != "" {
			user.Set("name", name)
		}
		// Auth records require a password; the user authenticates via OTP only,
		// so set a random one they never use.
		user.SetPassword(security.RandomString(40))
		if err := s.app.Save(user); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	} else if name != "" && user.GetString("name") == "" {
		// Backfill the name for an existing user that doesn't have one yet.
		user.Set("name", name)
		if err := s.app.Save(user); err != nil {
			return nil, fmt.Errorf("failed to update user: %w", err)
		}
	}

	code := security.RandomStringWithAlphabet(model.OTPCodeLength, model.OTPCodeAlphabet)

	otp := core.NewOTP(s.app)
	otp.SetCollectionRef(usersCollection.Id)
	otp.SetRecordRef(user.Id)
	otp.SetSentTo(email)
	otp.SetPassword(code)
	if err := s.app.Save(otp); err != nil {
		return nil, fmt.Errorf("failed to create OTP: %w", err)
	}

	if err := s.email.SendOTP(email, user.GetString("name"), code); err != nil {
		// Roll back the OTP so a failed send doesn't leave a dangling code.
		if delErr := s.app.Delete(otp); delErr != nil {
			s.app.Logger().Error("failed to delete OTP after email failure", "error", delErr, "otpId", otp.Id)
		}
		return nil, err
	}

	return &model.SendOTPResponse{
		OtpID:   otp.Id,
		Message: "Verification code sent",
	}, nil
}

// VerifyOTP validates the submitted code against the issued OTP. On success it
// marks the user verified, deletes the used OTP, and returns the user.
func (s *otpService) VerifyOTP(req *model.VerifyOTPRequest) (*model.VerifyOTPResponse, error) {
	otpID := strings.TrimSpace(req.OtpID)
	code := strings.TrimSpace(req.Code)
	if otpID == "" || code == "" {
		return nil, fmt.Errorf("otpId and code are required")
	}

	otp, err := s.app.FindOTPById(otpID)
	if err != nil {
		return nil, fmt.Errorf("invalid or expired code")
	}

	if otp.HasExpired(model.OTPExpiry) {
		if delErr := s.app.Delete(otp); delErr != nil {
			s.app.Logger().Error("failed to delete expired OTP", "error", delErr, "otpId", otp.Id)
		}
		return nil, fmt.Errorf("invalid or expired code")
	}

	if !otp.ValidatePassword(code) {
		return nil, fmt.Errorf("invalid or expired code")
	}

	user, err := s.app.FindRecordById(otp.CollectionRef(), otp.RecordRef())
	if err != nil {
		return nil, fmt.Errorf("invalid or expired code")
	}

	if !user.Verified() {
		user.SetVerified(true)
		if err := s.app.Save(user); err != nil {
			return nil, fmt.Errorf("failed to verify user: %w", err)
		}
	}

	// Single-use: drop the OTP once consumed.
	if delErr := s.app.Delete(otp); delErr != nil {
		s.app.Logger().Error("failed to delete used OTP", "error", delErr, "otpId", otp.Id)
	}

	return &model.VerifyOTPResponse{
		UserID:   user.Id,
		Email:    user.Email(),
		Name:     user.GetString("name"),
		Verified: true,
		Message:  "Verification successful",
	}, nil
}
