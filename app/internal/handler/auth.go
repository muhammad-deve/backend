package handler

import (
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

// SendOTPHandler issues an OTP code to the provided email and creates the user
// (unverified) if it doesn't exist yet.
//
// To avoid leaking whether an email exists or whether delivery succeeded, the
// response is uniform: any non-rate-limit failure still returns 200 with a
// generic message (and a throwaway otpId). Only throttling returns a 429.
func (h *Handler) SendOTPHandler(e *core.RequestEvent) error {
	req := &model.SendOTPRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	resp, err := h.service.OTP().SendOTP(req)
	if err != nil {
		if rl, ok := service.IsRateLimited(err); ok {
			e.Response.Header().Set("Retry-After", strconv.Itoa(rl.RetryAfterSeconds()))
			return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many requests. Please try again later.")
		}

		// Log the real cause server-side, but don't reveal it to the caller.
		h.logger.Error("failed to send OTP", "error", err, "email", req.Email)
		return h.NewSuccessResponse(e, http.StatusOK, &model.SendOTPResponse{
			OtpID:   core.GenerateDefaultRandomId(),
			Message: "If the email is valid, a verification code has been sent",
		})
	}

	resp.Message = "If the email is valid, a verification code has been sent"
	return h.NewSuccessResponse(e, http.StatusOK, resp)
}

// VerifyOTPHandler verifies the submitted OTP code. On success the linked user
// is marked verified.
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
