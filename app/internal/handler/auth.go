package handler

import (
	"github.com/pocketbase/pocketbase/core"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/model"
	"net/http"
)

func (h *Handler) PasswordResetOTPConfirmHandler(e *core.RequestEvent) error {
	body := &model.PasswordResetOTPConfirmRequest{}
	err := e.BindBody(body)
	if err != nil {
		return err
	}

	if body.OtpId == "" || body.Password == "" {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request")
	}

	token, err := h.service.Authorization().ResetPasswordOTPConfirm(body)
	if err != nil {
		return h.NewErrorResponse(e, http.StatusInternalServerError, err.Error())
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"token": token})
}

func (h *Handler) RecalculateViewsOfBook(e *core.RequestEvent) error {
	id := e.Request.PathValue("id")

	err := h.service.Book().IncrementBookViews(id)
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request")
	}

	return h.NewSuccessResponse(e, http.StatusOK, "ok")

}
