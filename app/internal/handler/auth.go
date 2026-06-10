package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

// SendOTPHandler starts signup: it issues an OTP to the provided email and
// creates a pending user. Already-registered emails are rejected with 409 so
// the UI can tell the user to log in instead.
func (h *Handler) SendOTPHandler(e *core.RequestEvent) error {
	req := &model.SendOTPRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	resp, err := h.service.OTP().SendOTP(req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrEmailRegistered):
			return h.NewErrorResponse(e, http.StatusConflict, "This email is already registered. Please log in instead.")
		default:
			if rl, ok := service.IsRateLimited(err); ok {
				e.Response.Header().Set("Retry-After", strconv.Itoa(rl.RetryAfterSeconds()))
				return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many requests. Please try again later.")
			}
			h.logger.Error("failed to send OTP", "error", err, "email", req.Email)
			return h.NewErrorResponse(e, http.StatusBadRequest, "Couldn't send the verification code. Please try again.")
		}
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}

// VerifyOTPHandler checks an OTP code without consuming it. On success the
// client advances to the set-password step.
func (h *Handler) VerifyOTPHandler(e *core.RequestEvent) error {
	req := &model.VerifyOTPRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	resp, err := h.service.OTP().VerifyOTP(req)
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}

// CompleteRegistrationHandler finalizes signup by setting the account password
// after the OTP has been verified.
func (h *Handler) CompleteRegistrationHandler(e *core.RequestEvent) error {
	req := &model.CompleteRegistrationRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	resp, err := h.service.OTP().CompleteRegistration(req)
	if err != nil {
		if errors.Is(err, service.ErrEmailRegistered) {
			return h.NewErrorResponse(e, http.StatusConflict, "This email is already registered. Please log in instead.")
		}
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}

// ForgotPasswordHandler issues a password-reset OTP for an existing account.
// The response is uniform regardless of whether the email exists.
func (h *Handler) ForgotPasswordHandler(e *core.RequestEvent) error {
	req := &model.ForgotPasswordRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	resp, err := h.service.OTP().ForgotPassword(req)
	if err != nil {
		if rl, ok := service.IsRateLimited(err); ok {
			e.Response.Header().Set("Retry-After", strconv.Itoa(rl.RetryAfterSeconds()))
			return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many requests. Please try again later.")
		}
		h.logger.Error("failed to send reset OTP", "error", err, "email", req.Email)
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}

// ResetPasswordHandler sets a new password after the reset OTP is verified.
func (h *Handler) ResetPasswordHandler(e *core.RequestEvent) error {
	req := &model.ResetPasswordRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	resp, err := h.service.OTP().ResetPassword(req)
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}
