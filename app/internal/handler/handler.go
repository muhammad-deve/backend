package handler

import (
	"log/slog"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

type Handler struct {
	logger  *slog.Logger
	service service.I
	cfg     *config.Config
}

func (h *Handler) Register(router *router.Router[*core.RequestEvent]) {
	router.BindFunc(func(e *core.RequestEvent) error {
		handled, err := h.service.TCP().HandleTunnelRequest(e)
		if handled || err != nil {
			return err
		}
		return e.Next()
	})

	api := router.Group("/api/v1")
	{
		auth := api.Group("/auth")
		{
			auth.POST("/send-otp", h.SendOTPHandler)
			auth.POST("/verify-otp", h.VerifyOTPHandler)
			auth.POST("/complete-registration", h.CompleteRegistrationHandler)
			auth.POST("/forgot-password", h.ForgotPasswordHandler)
			auth.POST("/reset-password", h.ResetPasswordHandler)
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
