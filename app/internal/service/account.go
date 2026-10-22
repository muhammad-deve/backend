package service

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/security"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/pkg/disposable"
)

var (
	ErrEmailTaken               = errors.New("this email is already in use")
	ErrCurrentPasswordIncorrect = errors.New("current password is incorrect")
)

type AccountI interface {
	RequestEmailChange(user *core.Record, req *model.RequestEmailChangeRequest) (*model.SendOTPResponse, error)
	ConfirmEmailChange(user *core.Record, req *model.ConfirmEmailChangeRequest) (*model.EmailChangeResponse, error)
	ChangePassword(user *core.Record, req *model.ChangePasswordRequest) (*model.ChangePasswordResponse, error)
	// ReleasePending clears what others may have attached to an unverified
	// account before its owner signs in with a provider.
	ReleasePending(user *core.Record, provider, providerID string) error
}

type accountService struct {
	app      core.App
	email    EmailI
	tokens   TokensI
	limiter  *rateLimiter
	attempts *otpAttempts

	// pending maps an email-change OTP id to the address it was sent to, so
	// only codes issued for an email change can confirm one.
	pendingMu sync.Mutex
	pending   map[string]pendingEmailChange
}

type pendingEmailChange struct {
	userID string
	email  string
	at     time.Time
}

func NewAccountService(app core.App, email EmailI, tokens TokensI, attempts *otpAttempts) AccountI {
	return &accountService{
		app:      app,
		email:    email,
		tokens:   tokens,
		limiter:  newRateLimiter(time.Hour, 5, 60*time.Second),
		attempts: attempts,
		pending:  make(map[string]pendingEmailChange),
	}
}

func (s *accountService) rememberChange(otpID, userID, email string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	now := time.Now()
	for id, change := range s.pending {
		if now.Sub(change.at) > model.OTPExpiry {
			delete(s.pending, id)
		}
	}
	s.pending[otpID] = pendingEmailChange{userID: userID, email: email, at: now}
}

func (s *accountService) pendingChange(otpID string) (pendingEmailChange, bool) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	change, ok := s.pending[otpID]
	return change, ok
}

func (s *accountService) forgetChange(otpID string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	delete(s.pending, otpID)
}

func (s *accountService) ReleasePending(user *core.Record, provider, providerID string) error {
	return releasePendingAccount(s.app, s.tokens, user, provider, providerID)
}

func (s *accountService) RequestEmailChange(user *core.Record, req *model.RequestEmailChangeRequest) (*model.SendOTPResponse, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("a valid email is required")
	}
	if strings.EqualFold(email, user.Email()) {
		return nil, fmt.Errorf("enter a different email address")
	}
	if disposable.Email(email) {
		return nil, ErrDisposableEmail
	}

	usersCollection, err := s.app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		return nil, fmt.Errorf("users collection not found: %w", err)
	}
	existing, err := s.app.FindAuthRecordByEmail(usersCollection, email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check email: %w", err)
	}
	if existing != nil && existing.Id != user.Id {
		return nil, ErrEmailTaken
	}

	if allowed, retryAfter := s.limiter.Allow("email-change:" + user.Id + ":" + email); !allowed {
		return nil, &errRateLimited{retryAfter: retryAfter}
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
		if delErr := s.app.Delete(otp); delErr != nil {
			s.app.Logger().Error("failed to delete email-change OTP", "error", delErr, "otpId", otp.Id)
		}
		return nil, err
	}
	s.rememberChange(otp.Id, user.Id, email)

	return &model.SendOTPResponse{OtpID: otp.Id, Message: "Verification code sent"}, nil
}

func (s *accountService) ConfirmEmailChange(user *core.Record, req *model.ConfirmEmailChangeRequest) (*model.EmailChangeResponse, error) {
	usersCollection, err := s.app.FindCollectionByNameOrId(model.UsersCollection)
	if err != nil {
		return nil, fmt.Errorf("users collection not found: %w", err)
	}
	// Only a code this user asked for an email change, sent to that new
	// address, is accepted. Sign-up and reset codes go to the current address.
	otp, err := checkOTPCode(s.app, s.attempts, req.OtpID, req.Code, func(otp *core.OTP) bool {
		change, ok := s.pendingChange(otp.Id)
		return ok &&
			otp.CollectionRef() == usersCollection.Id &&
			otp.RecordRef() == user.Id &&
			change.userID == user.Id &&
			sameEmail(otp.SentTo(), change.email) &&
			!sameEmail(otp.SentTo(), user.Email())
	})
	if err != nil {
		return nil, err
	}

	newEmail := strings.ToLower(strings.TrimSpace(otp.SentTo()))
	existing, lookupErr := s.app.FindAuthRecordByEmail(usersCollection, newEmail)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to check email: %w", lookupErr)
	}
	if existing != nil && existing.Id != user.Id {
		return nil, ErrEmailTaken
	}

	oldEmail := user.Email()
	user.SetEmail(newEmail)
	user.SetVerified(true)
	if err := s.app.Save(user); err != nil {
		return nil, fmt.Errorf("failed to change email: %w", err)
	}
	s.forgetChange(otp.Id)
	if delErr := s.app.Delete(otp); delErr != nil {
		s.app.Logger().Error("failed to delete used email-change OTP", "error", delErr, "otpId", otp.Id)
	}
	// Tell the old address, in case the session was stolen.
	if err := s.email.SendEmailChanged(oldEmail, user.GetString("name"), newEmail); err != nil {
		s.app.Logger().Error("failed to send email-change notice", "error", err, "userId", user.Id)
	}
	token, err := user.NewAuthToken()
	if err != nil {
		return nil, fmt.Errorf("email changed but a new session couldn't be created: %w", err)
	}

	return &model.EmailChangeResponse{Email: newEmail, Token: token, Message: "Email changed"}, nil
}

func (s *accountService) ChangePassword(user *core.Record, req *model.ChangePasswordRequest) (*model.ChangePasswordResponse, error) {
	if !user.ValidatePassword(req.CurrentPassword) {
		return nil, ErrCurrentPasswordIncorrect
	}
	if req.NewPassword != req.ConfirmPassword {
		return nil, fmt.Errorf("new passwords do not match")
	}
	if ok, reason := model.ValidatePassword(req.NewPassword); !ok {
		return nil, fmt.Errorf("%s", reason)
	}
	if user.ValidatePassword(req.NewPassword) {
		return nil, fmt.Errorf("new password must be different from the current password")
	}

	user.SetPassword(req.NewPassword)
	if err := s.app.Save(user); err != nil {
		return nil, fmt.Errorf("failed to change password: %w", err)
	}
	token, err := user.NewAuthToken()
	if err != nil {
		return nil, fmt.Errorf("password changed but a new session couldn't be created: %w", err)
	}

	return &model.ChangePasswordResponse{Token: token, Message: "Password changed"}, nil
}
