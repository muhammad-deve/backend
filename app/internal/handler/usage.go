package handler

import (
	"errors"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

func (h *Handler) UsageHandler(e *core.RequestEvent) error {
	user := e.Auth
	if user == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}

	response, err := h.service.Usage().Get(user.Id, e.Request.URL.Query().Get("range"))
	if errors.Is(err, service.ErrInvalidUsageRange) {
		return h.NewErrorResponse(e, http.StatusBadRequest, "range must be hour, day, or week")
	}
	if err != nil {
		h.logger.Error("failed to load usage history", "error", err, "userId", user.Id)
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't load usage history. Please try again.")
	}

	return h.NewSuccessResponse(e, http.StatusOK, response)
}
