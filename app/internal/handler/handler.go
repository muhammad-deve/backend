package handler

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/config"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/service"
	"log/slog"
)

type Handler struct {
	logger  *slog.Logger
	service service.I
	cfg     *config.Config
}

func (h *Handler) Register(router *router.Router[*core.RequestEvent]) {
	api := router.Group("/api/v1")
	{
		auth := api.Group("/auth")
		{
			auth.POST("/password-reset-otp-confirm", h.PasswordResetOTPConfirmHandler)
		}

	}
}

func NewHandler(logger *slog.Logger, service service.I, cfg *config.Config) *Handler {
	return &Handler{
		logger:  logger,
		service: service,
		cfg:     cfg,
	}
}
