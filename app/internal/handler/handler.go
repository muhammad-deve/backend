package handler

import (
	"log/slog"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/config"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

type Handler struct {
	logger         *slog.Logger
	service        service.I
	cfg            *config.Config
	shareUploads   chan struct{}
	shareDownloads chan struct{}
	shareAttempts  *shareAttemptLimiter
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
			auth.POST("/verify-token", h.VerifyTokenHandler)
			auth.POST("/cli-plan", h.CLIPlanHandler)
		}

		dashboard := api.Group("/dashboard")
		{
			dashboard.Bind(apis.RequireAuth("users"))
			dashboard.GET("", h.DashboardHandler)
			dashboard.GET("/usage", h.UsageHandler)
		}

		account := api.Group("/account")
		{
			account.Bind(apis.RequireAuth("users"))
			account.POST("/email/request", h.RequestEmailChangeHandler)
			account.POST("/email/confirm", h.ConfirmEmailChangeHandler)
			account.POST("/password", h.ChangePasswordHandler)
		}

		tokens := api.Group("/tokens")
		{
			tokens.Bind(apis.RequireAuth("users"))
			tokens.GET("", h.ListTokensHandler)
			tokens.POST("", h.CreateTokenHandler)
			tokens.DELETE("/{id}", h.DeleteTokenHandler)
		}

		tunnels := api.Group("/tunnels")
		{
			tunnels.Bind(apis.RequireAuth("users"))
			tunnels.POST("/{subdomain}/stop", h.StopTunnelHandler)
			tunnels.DELETE("/{subdomain}", h.DeleteTunnelHandler)
		}

		shares := api.Group("/shares")
		{
			shares.Bind(apis.RequireAuth("users"))
			shares.GET("", h.ListSharesHandler)
			shares.POST("", h.UploadShareHandler)
			shares.PATCH("/{id}", h.UpdateShareHandler)
			shares.POST("/{id}/revoke", h.RevokeShareHandler)
			shares.DELETE("/{id}", h.DeleteShareHandler)
		}

		publicShares := api.Group("/public/shares")
		{
			publicShares.OPTIONS("/{token}", h.PublicShareOptionsHandler)
			publicShares.OPTIONS("/{token}/unlock", h.PublicShareOptionsHandler)
			publicShares.OPTIONS("/{token}/content", h.PublicShareOptionsHandler)
			publicShares.GET("/{token}", h.PublicShareHandler)
			publicShares.POST("/{token}/unlock", h.UnlockShareHandler)
			publicShares.GET("/{token}/content", h.ShareContentHandler)
			publicShares.HEAD("/{token}/content", h.ShareContentHandler)
		}

		api.POST("/billing/webhook", h.LemonSqueezyWebhookHandler)
		billing := api.Group("/billing")
		{
			billing.Bind(apis.RequireAuth("users"))
			billing.GET("/portal", h.BillingPortalHandler)
			billing.POST("/checkout", h.CreateCheckoutHandler)
			billing.PATCH("/subscription", h.ChangeSubscriptionPlanHandler)
			billing.DELETE("/subscription", h.CancelSubscriptionHandler)
		}
	}
}

func NewHandler(logger *slog.Logger, service service.I, cfg *config.Config) *Handler {
	return &Handler{logger: logger, service: service, cfg: cfg, shareUploads: make(chan struct{}, 2), shareDownloads: make(chan struct{}, 32), shareAttempts: &shareAttemptLimiter{hits: make(map[string][]time.Time)}}
}

type shareAttemptLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}
