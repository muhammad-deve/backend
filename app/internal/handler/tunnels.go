package handler

import (
	"errors"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

func (h *Handler) StopTunnelHandler(e *core.RequestEvent) error {
	user := e.Auth
	if user == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}

	subdomain := e.Request.PathValue("subdomain")
	if err := h.service.TCP().StopTunnel(user.Id, subdomain); err != nil {
		switch {
		case errors.Is(err, service.ErrTunnelNotFound):
			return h.NewErrorResponse(e, http.StatusNotFound, err.Error())
		case errors.Is(err, service.ErrTunnelInactive):
			return h.NewErrorResponse(e, http.StatusConflict, err.Error())
		default:
			h.logger.Error("failed to stop tunnel", "error", err, "subdomain", subdomain)
			return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't stop the tunnel.")
		}
	}

	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"status": "stopped"})
}

func (h *Handler) DeleteTunnelHandler(e *core.RequestEvent) error {
	user := e.Auth
	if user == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}

	subdomain := e.Request.PathValue("subdomain")
	if err := h.service.TCP().DeleteTunnel(user.Id, subdomain); err != nil {
		switch {
		case errors.Is(err, service.ErrTunnelNotFound):
			return h.NewErrorResponse(e, http.StatusNotFound, err.Error())
		case errors.Is(err, service.ErrTunnelActive):
			return h.NewErrorResponse(e, http.StatusConflict, err.Error())
		default:
			h.logger.Error("failed to delete tunnel", "error", err, "subdomain", subdomain)
			return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't delete the tunnel.")
		}
	}

	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"status": "deleted"})
}
