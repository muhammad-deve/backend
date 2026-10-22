package service

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/pkg/disposable"
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

// ErrDisposableEmail is returned for throwaway ("temp mail") addresses, which
// would otherwise let one person open unlimited accounts.
var ErrDisposableEmail = errors.New("disposable email addresses are not accepted")

type OTPI interface {
	SendOTP(req *model.SendOTPRequest) (*model.SendOTPResponse, error)
	VerifyOTP(req *model.VerifyOTPRequest) (*model.VerifyOTPResponse, error)
	CompleteRegistration(req *model.CompleteRegistrationRequest) (*model.CompleteRegistrationResponse, error)
	ForgotPassword(req *model.ForgotPasswordRequest) (*model.SendOTPResponse, error)
	ResetPassword(req *model.ResetPasswordRequest) (*model.ResetPasswordResponse, error)
}

type otpService struct {
	app      core.App
	email    EmailI
	tokens   TokensI
	limiter  *rateLimiter
	attempts *otpAttempts
}

func NewOTPService(app core.App, email EmailI, tokens TokensI, attempts *otpAttempts) OTPI {
	return &otpService{
		app:    app,
		email:  email,
		tokens: tokens,
		// Max 5 codes per email per hour, with a 60s cooldown between sends.
		limiter:  newRateLimiter(time.Hour, 5, 60*time.Second),
		attempts: attempts,
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
	if disposable.Email(email) {
		return nil, ErrDisposableEmail
	}
	name, err := model.CleanName(req.Name, model.MaxNameLength)
	if err != nil {
		return nil, userError(err.Error())
	}

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
	_, user, err := s.checkOwnEmailCode(req.OtpID, req.Code)
	if err != nil {
		return nil, err
	}
	if user.Verified() {
		return nil, ErrEmailRegistered
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

	otp, user, err := s.checkOwnEmailCode(req.OtpID, req.Code)
	if err != nil {
		return nil, err
	}
	if user.Verified() {
		return nil, ErrEmailRegistered
	}
	// The account may have existed before this sign-up, so drop anything
	// someone else attached to it and end their sessions.
	if err := releasePendingAccount(s.app, s.tokens, user, "", ""); err != nil {
		return nil, fmt.Errorf("failed to prepare account: %w", err)
	}
	user.SetPassword(req.Password)
	user.RefreshTokenKey()
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

	otp, user, err := s.checkOwnEmailCode(req.OtpID, req.Code)
	if err != nil {
		return nil, err
	}
	// Reset codes are issued only to verified accounts. A sign-up code must
	// not set a password here.
	if !user.Verified() {
		return nil, fmt.Errorf("invalid or expired code")
	}

	// Changing the password also ends every other session.
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

// ErrTooManyCodeAttempts is returned once an OTP has had too many wrong codes.
// The OTP is deleted at that point and a new code must be requested.
var ErrTooManyCodeAttempts = errors.New("Too many wrong codes. Please request a new code.")

// checkOwnEmailCode checks a sign-up or password-reset code. Those codes are
// sent to the account's own address, so a code sent anywhere else (an email
// change code goes to the new address) is refused.
func (s *otpService) checkOwnEmailCode(otpID, code string) (*core.OTP, *core.Record, error) {
	users, err := s.app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		return nil, nil, fmt.Errorf("users collection not found: %w", err)
	}
	var user *core.Record
	otp, err := checkOTPCode(s.app, s.attempts, otpID, code, func(otp *core.OTP) bool {
		if otp.CollectionRef() != users.Id {
			return false
		}
		record, err := s.app.FindRecordById(users, otp.RecordRef())
		if err != nil || !sameEmail(otp.SentTo(), record.Email()) {
			return false
		}
		user = record
		return true
	})
	if err != nil {
		return nil, nil, err
	}
	return otp, user, nil
}

func sameEmail(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && strings.EqualFold(a, b)
}

// checkOTPCode loads an OTP by id and checks the code and expiry. accept, if
// set, decides whether the OTP may be used here; it runs before the code is
// checked, so a refused OTP doesn't use up a try. Expired OTPs are deleted,
// and so is an OTP after maxWrongCodesPerOTP wrong codes. It does not consume
// a valid OTP.
func checkOTPCode(app core.App, attempts *otpAttempts, otpID, code string, accept func(*core.OTP) bool) (*core.OTP, error) {
	otpID = strings.TrimSpace(otpID)
	code = strings.TrimSpace(code)
	if otpID == "" || code == "" {
		return nil, fmt.Errorf("otpId and code are required")
	}

	otp, err := app.FindOTPById(otpID)
	if err != nil {
		return nil, fmt.Errorf("invalid or expired code")
	}

	if otp.HasExpired(model.OTPExpiry) {
		deleteOTP(app, otp, "expired")
		attempts.forget(otp.Id)
		return nil, fmt.Errorf("invalid or expired code")
	}
	if accept != nil && !accept(otp) {
		return nil, fmt.Errorf("invalid or expired code")
	}

	if !attempts.begin(otp.Id) {
		deleteOTP(app, otp, "too many wrong codes")
		return nil, ErrTooManyCodeAttempts
	}
	if !otp.ValidatePassword(code) {
		if attempts.exhausted(otp.Id) {
			deleteOTP(app, otp, "too many wrong codes")
		}
		return nil, fmt.Errorf("invalid or expired code")
	}
	attempts.release(otp.Id)

	return otp, nil
}

func deleteOTP(app core.App, otp *core.OTP, reason string) {
	if err := app.Delete(otp); err != nil {
		app.Logger().Error("failed to delete OTP", "error", err, "otpId", otp.Id, "reason", reason)
	}
}
