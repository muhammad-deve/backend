package handler

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// DashboardHandler returns the authenticated user's CLI token, aggregate
// traffic stats, and the subdomains they own. It powers the landing dashboard.
func (h *Handler) DashboardHandler(e *core.RequestEvent) error {
	user := e.Auth
	if user == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}

	resp, err := h.service.Dashboard().GetDashboard(user)
	if err != nil {
		h.logger.Error("failed to build dashboard", "error", err, "userId", user.Id)
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't load your dashboard. Please try again.")
	}

	return h.NewSuccessResponse(e, http.StatusOK, resp)
}
