package handler

import (
	"errors"
	"net/http"

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

	if err := h.service.Throttle().CodeSend(ClientIP(e.Request)); err != nil {
		return h.limitedResponse(e, err)
	}

	resp, err := h.service.Account().RequestEmailChange(e.Auth, req)
	if err != nil {
		if _, ok := service.IsRateLimited(err); ok {
			return h.limitedResponse(e, err)
		}
		if errors.Is(err, service.ErrEmailTaken) {
			return h.NewErrorResponse(e, http.StatusConflict, "This email is already in use.")
		}
		if errors.Is(err, service.ErrDisposableEmail) {
			return h.NewErrorResponse(e, http.StatusBadRequest, disposableEmailMessage)
		}
		return h.errorResponse(e, err, "Couldn't send the verification code. Please try again.", "failed to request email change", "userId", e.Auth.Id)
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

	if err := h.service.Throttle().CodeCheck(ClientIP(e.Request)); err != nil {
		return h.limitedResponse(e, err)
	}

	resp, err := h.service.Account().ConfirmEmailChange(e.Auth, req)
	if err != nil {
		if errors.Is(err, service.ErrEmailTaken) {
			return h.NewErrorResponse(e, http.StatusConflict, "This email is already in use.")
		}
		return h.errorResponse(e, err, "Couldn't change your email. Please try again.", "failed to confirm email change", "userId", e.Auth.Id)
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

	ip := ClientIP(e.Request)
	if err := h.service.Throttle().LoginAllowed(ip, e.Auth.Email()); err != nil {
		return h.limitedResponse(e, err)
	}

	resp, err := h.service.Account().ChangePassword(e.Auth, req)
	if err != nil {
		if errors.Is(err, service.ErrCurrentPasswordIncorrect) {
			h.service.Throttle().LoginFailed(ip, e.Auth.Email())
			return h.NewErrorResponse(e, http.StatusBadRequest, service.ErrCurrentPasswordIncorrect.Error())
		}
		return h.errorResponse(e, err, "Couldn't change your password. Please try again.", "failed to change password", "userId", e.Auth.Id)
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}

// LogoutHandler ends every dashboard session of the signed-in account.
// Session tokens are signed with the account's token key, so a new key makes
// all of them invalid, including copies taken from a browser. CLI tokens are
// separate and keep working.
func (h *Handler) LogoutHandler(e *core.RequestEvent) error {
	record := e.Auth
	if record == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "Sign in first.")
	}
	record.RefreshTokenKey()
	if err := e.App.SaveNoValidate(record); err != nil {
		e.App.Logger().Error("failed to end sessions on logout", "error", err, "userId", record.Id)
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't sign out. Please try again.")
	}
	return e.NoContent(http.StatusNoContent)
}
