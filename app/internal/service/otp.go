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

// ErrEmailRegistered is returned when signup is attempted for an email that
// already has a completed (verified) account.
var ErrEmailRegistered = errors.New("this email is already registered")

type OTPI interface {
	SendOTP(req *model.SendOTPRequest) (*model.SendOTPResponse, error)
	VerifyOTP(req *model.VerifyOTPRequest) (*model.VerifyOTPResponse, error)
	CompleteRegistration(req *model.CompleteRegistrationRequest) (*model.CompleteRegistrationResponse, error)
	ForgotPassword(req *model.ForgotPasswordRequest) (*model.SendOTPResponse, error)
	ResetPassword(req *model.ResetPasswordRequest) (*model.ResetPasswordResponse, error)
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

// SendOTP starts signup: it rejects emails that are already registered,
// finds-or-creates a pending (unverified) user, issues a native PocketBase OTP
// linked to that user, emails the code, and returns the OTP id.
func (s *otpService) SendOTP(req *model.SendOTPRequest) (*model.SendOTPResponse, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("a valid email is required")
	}
	name := strings.TrimSpace(req.Name)

	usersCollection, err := s.app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		return nil, fmt.Errorf("users collection not found: %w", err)
	}

	// Reject already-registered emails up front. A user is considered
	// registered once they've completed signup (verified == true).
	existing, err := s.app.FindAuthRecordByEmail(usersCollection, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to lookup user: %w", err)
	}
	if existing != nil && existing.Verified() {
		return nil, ErrEmailRegistered
	}

	// Throttle before doing further work so abuse can't drive user creation or
	// email sends. Keyed by email; resets on restart.
	if allowed, retryAfter := s.limiter.Allow(email); !allowed {
		return nil, &errRateLimited{retryAfter: retryAfter}
	}

	// Find or create the pending auth record. The native _otps collection
	// requires recordRef to point at an existing auth record, so the user must
	// exist before an OTP can be stored.
	user := existing
	if user == nil {
		user = core.NewRecord(usersCollection)
		user.SetEmail(email)
		if name != "" {
			user.Set("name", name)
		}
		// The real password is set later in CompleteRegistration; use a random
		// placeholder so the auth record validates in the meantime.
		user.SetPassword(security.RandomString(40))
		if err := s.app.Save(user); err != nil {
			return nil, fmt.Errorf("failed to create user: %w", err)
		}
	} else if name != "" && user.GetString("name") != name {
		// Keep the latest name for a pending (unverified) signup.
		user.Set("name", name)
		if err := s.app.Save(user); err != nil {
			return nil, fmt.Errorf("failed to update user: %w", err)
		}
	}

	return s.issueOTP(usersCollection.Id, user)
}

// issueOTP creates a fresh OTP for the user, emails the code, and returns the
// OTP id. The OTP is rolled back if the email send fails.
func (s *otpService) issueOTP(collectionID string, user *core.Record) (*model.SendOTPResponse, error) {
	code := security.RandomStringWithAlphabet(model.OTPCodeLength, model.OTPCodeAlphabet)

	otp := core.NewOTP(s.app)
	otp.SetCollectionRef(collectionID)
	otp.SetRecordRef(user.Id)
	otp.SetSentTo(user.Email())
	otp.SetPassword(code)
	if err := s.app.Save(otp); err != nil {
		return nil, fmt.Errorf("failed to create OTP: %w", err)
	}

	if err := s.email.SendOTP(user.Email(), user.GetString("name"), code); err != nil {
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

// VerifyOTP validates the submitted code without consuming it. The OTP remains
// valid so it can be re-checked by CompleteRegistration when the password is
// set. Returns the pending user's email and name on success.
func (s *otpService) VerifyOTP(req *model.VerifyOTPRequest) (*model.VerifyOTPResponse, error) {
	otp, err := s.checkOTP(req.OtpID, req.Code)
	if err != nil {
		return nil, err
	}

	user, err := s.app.FindRecordById(otp.CollectionRef(), otp.RecordRef())
	if err != nil {
		return nil, fmt.Errorf("invalid or expired code")
	}

	return &model.VerifyOTPResponse{
		Email:   user.Email(),
		Name:    user.GetString("name"),
		Valid:   true,
		Message: "Code verified",
	}, nil
}

// CompleteRegistration re-validates the OTP, applies the chosen password, marks
// the user verified, and consumes the OTP. This is the final signup step.
func (s *otpService) CompleteRegistration(req *model.CompleteRegistrationRequest) (*model.CompleteRegistrationResponse, error) {
	if ok, reason := model.ValidatePassword(req.Password); !ok {
		return nil, fmt.Errorf("%s", reason)
	}

	otp, err := s.checkOTP(req.OtpID, req.Code)
	if err != nil {
		return nil, err
	}

	user, err := s.app.FindRecordById(otp.CollectionRef(), otp.RecordRef())
	if err != nil {
		return nil, fmt.Errorf("invalid or expired code")
	}

	if user.Verified() {
		return nil, ErrEmailRegistered
	}
	user.SetPassword(req.Password)
	user.SetVerified(true)
	if err := s.app.Save(user); err != nil {
		return nil, fmt.Errorf("failed to complete registration: %w", err)
	}

	// Consume the OTP now that signup is finished.
	if delErr := s.app.Delete(otp); delErr != nil {
		s.app.Logger().Error("failed to delete used OTP", "error", delErr, "otpId", otp.Id)
	}

	return &model.CompleteRegistrationResponse{
		UserID:  user.Id,
		Email:   user.Email(),
		Name:    user.GetString("name"),
		Message: "Account created",
	}, nil
}

// ForgotPassword issues a password-reset OTP for an existing, verified account.
// To avoid leaking which emails exist, it returns a successful-looking response
// even when no matching account is found (no email is sent in that case).
func (s *otpService) ForgotPassword(req *model.ForgotPasswordRequest) (*model.SendOTPResponse, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("a valid email is required")
	}

	usersCollection, err := s.app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		return nil, fmt.Errorf("users collection not found: %w", err)
	}

	if allowed, retryAfter := s.limiter.Allow("reset:" + email); !allowed {
		return nil, &errRateLimited{retryAfter: retryAfter}
	}

	user, err := s.app.FindAuthRecordByEmail(usersCollection, email)
	if err != nil || user == nil || !user.Verified() {
		// Don't reveal whether the account exists. Return a generic OK with a
		// throwaway otpId so the UI can advance to the code step uniformly.
		return &model.SendOTPResponse{
			OtpID:   core.GenerateDefaultRandomId(),
			Message: "If the email is registered, a reset code has been sent",
		}, nil
	}

	return s.issueOTP(usersCollection.Id, user)
}

// ResetPassword re-validates the OTP and sets a new password for the account.
func (s *otpService) ResetPassword(req *model.ResetPasswordRequest) (*model.ResetPasswordResponse, error) {
	if ok, reason := model.ValidatePassword(req.Password); !ok {
		return nil, fmt.Errorf("%s", reason)
	}

	otp, err := s.checkOTP(req.OtpID, req.Code)
	if err != nil {
		return nil, err
	}

	user, err := s.app.FindRecordById(otp.CollectionRef(), otp.RecordRef())
	if err != nil {
		return nil, fmt.Errorf("invalid or expired code")
	}

	user.SetPassword(req.Password)
	if err := s.app.Save(user); err != nil {
		return nil, fmt.Errorf("failed to reset password: %w", err)
	}

	// Consume the OTP now that the reset is complete.
	if delErr := s.app.Delete(otp); delErr != nil {
		s.app.Logger().Error("failed to delete used OTP", "error", delErr, "otpId", otp.Id)
	}

	return &model.ResetPasswordResponse{
		Email:   user.Email(),
		Message: "Password updated",
	}, nil
}

// checkOTP loads an OTP by id and validates the submitted code and expiry.
// Expired OTPs are deleted. It does not consume valid OTPs.
func (s *otpService) checkOTP(otpID, code string) (*core.OTP, error) {
	otpID = strings.TrimSpace(otpID)
	code = strings.TrimSpace(code)
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

	return otp, nil
}
