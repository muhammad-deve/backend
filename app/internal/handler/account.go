package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

func (h *Handler) RequestEmailChangeHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}
	req := &model.RequestEmailChangeRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	resp, err := h.service.Account().RequestEmailChange(e.Auth, req)
	if err != nil {
		if rl, ok := service.IsRateLimited(err); ok {
			e.Response.Header().Set("Retry-After", strconv.Itoa(rl.RetryAfterSeconds()))
			return h.NewErrorResponse(e, http.StatusTooManyRequests, "Too many requests. Please try again later.")
		}
		if errors.Is(err, service.ErrEmailTaken) {
			return h.NewErrorResponse(e, http.StatusConflict, "This email is already in use.")
		}
		h.logger.Error("failed to request email change", "error", err, "userId", e.Auth.Id)
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}

func (h *Handler) ConfirmEmailChangeHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}
	req := &model.ConfirmEmailChangeRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	resp, err := h.service.Account().ConfirmEmailChange(e.Auth, req)
	if err != nil {
		if errors.Is(err, service.ErrEmailTaken) {
			return h.NewErrorResponse(e, http.StatusConflict, "This email is already in use.")
		}
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}

func (h *Handler) ChangePasswordHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}
	req := &model.ChangePasswordRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	resp, err := h.service.Account().ChangePassword(e.Auth, req)
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}
