package handler

import (
	"errors"
	"io"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

const maxWebhookBodyBytes = 1 << 20

func (h *Handler) CreateCheckoutHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}
	request := &model.CheckoutRequest{}
	if err := e.BindBody(request); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	response, err := h.service.Billing().CreateCheckout(e.Request.Context(), e.Auth, request.Plan)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidBillingPlan):
			return h.NewErrorResponse(e, http.StatusBadRequest, "Choose a monthly or yearly Pro plan.")
		case errors.Is(err, service.ErrAlreadySubscribed):
			return h.NewErrorResponse(e, http.StatusConflict, err.Error())
		case errors.Is(err, service.ErrBillingNotConfigured):
			return h.NewErrorResponse(e, http.StatusServiceUnavailable, "Pro checkout is not configured yet.")
		default:
			h.logger.Error("failed to create Lemon Squeezy checkout", "error", err, "userId", e.Auth.Id)
			return h.NewErrorResponse(e, http.StatusBadGateway, "Couldn't start checkout. Please try again.")
		}
	}
	return h.NewSuccessResponse(e, http.StatusCreated, response)
}

func (h *Handler) BillingPortalHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}
	response, err := h.service.Billing().CustomerPortal(e.Request.Context(), e.Auth.Id)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNoSubscription):
			return h.NewErrorResponse(e, http.StatusNotFound, "No Lemon Squeezy subscription was found for this account.")
		case errors.Is(err, service.ErrBillingNotConfigured):
			return h.NewErrorResponse(e, http.StatusServiceUnavailable, "Billing management is not configured yet.")
		default:
			h.logger.Error("failed to open Lemon Squeezy portal", "error", err, "userId", e.Auth.Id)
			return h.NewErrorResponse(e, http.StatusBadGateway, "Couldn't open billing management. Please try again.")
		}
	}
	return h.NewSuccessResponse(e, http.StatusOK, response)
}

func (h *Handler) LemonSqueezyWebhookHandler(e *core.RequestEvent) error {
	e.Request.Body = http.MaxBytesReader(e.Response, e.Request.Body, maxWebhookBodyBytes)
	body, err := io.ReadAll(e.Request.Body)
	if err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid webhook body")
	}
	if err := h.service.Billing().ProcessWebhook(e.Request.Context(), body, e.Request.Header.Get("X-Signature")); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidWebhookSignature):
			return h.NewErrorResponse(e, http.StatusUnauthorized, "invalid webhook signature")
		case errors.Is(err, service.ErrBillingNotConfigured):
			return h.NewErrorResponse(e, http.StatusServiceUnavailable, "billing webhook is not configured")
		case errors.Is(err, service.ErrWebhookModeMismatch),
			errors.Is(err, service.ErrWebhookStoreMismatch),
			errors.Is(err, service.ErrWebhookUserNotFound):
			h.logger.Warn("rejected Lemon Squeezy webhook", "error", err)
			return h.NewErrorResponse(e, http.StatusUnprocessableEntity, err.Error())
		default:
			h.logger.Error("failed to process Lemon Squeezy webhook", "error", err)
			return h.NewErrorResponse(e, http.StatusInternalServerError, "webhook processing failed")
		}
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"message": "webhook processed"})
}
