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

func (h *Handler) ChangeSubscriptionPlanHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}
	request := &model.ChangePlanRequest{}
	if err := e.BindBody(request); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}
	if err := h.service.Billing().ChangeSubscriptionPlan(e.Request.Context(), e.Auth.Id, request.Plan); err != nil {
		switch {
		case errors.Is(err, service.ErrNoSubscription):
			return h.NewErrorResponse(e, http.StatusNotFound, "No active Lemon Squeezy subscription was found for this account.")
		case errors.Is(err, service.ErrPlanChangeUnavailable):
			return h.NewErrorResponse(e, http.StatusConflict, "Only an active monthly subscription can switch to yearly billing.")
		case errors.Is(err, service.ErrBillingNotConfigured):
			return h.NewErrorResponse(e, http.StatusServiceUnavailable, "Billing changes are not configured yet.")
		default:
			h.logger.Error("failed to change Lemon Squeezy subscription", "error", err, "userId", e.Auth.Id)
			return h.NewErrorResponse(e, http.StatusBadGateway, "Couldn't change your billing cycle. Please try again.")
		}
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"message": "Subscription changed to yearly billing."})
}

func (h *Handler) CancelSubscriptionHandler(e *core.RequestEvent) error {
	if e.Auth == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}
	if err := h.service.Billing().CancelSubscription(e.Request.Context(), e.Auth.Id); err != nil {
		switch {
		case errors.Is(err, service.ErrNoSubscription):
			return h.NewErrorResponse(e, http.StatusNotFound, "No active Lemon Squeezy subscription was found for this account.")
		case errors.Is(err, service.ErrSubscriptionCancelled):
			return h.NewErrorResponse(e, http.StatusConflict, "This subscription is already scheduled to end.")
		case errors.Is(err, service.ErrBillingNotConfigured):
			return h.NewErrorResponse(e, http.StatusServiceUnavailable, "Subscription cancellation is not configured yet.")
		default:
			h.logger.Error("failed to cancel Lemon Squeezy subscription", "error", err, "userId", e.Auth.Id)
			return h.NewErrorResponse(e, http.StatusBadGateway, "Couldn't cancel your subscription. Please try again.")
		}
	}
	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"message": "Subscription cancellation scheduled."})
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
