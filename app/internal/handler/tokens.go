package handler

import (
	"errors"
	"net/http"

	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

// ListTokensHandler returns all CLI tokens owned by the authenticated user.
func (h *Handler) ListTokensHandler(e *core.RequestEvent) error {
	user := e.Auth
	if user == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}

	tokens, err := h.service.Tokens().List(user.Id)
	if err != nil {
		h.logger.Error("failed to list tokens", "error", err, "userId", user.Id)
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't load your tokens.")
	}

	return h.NewSuccessResponse(e, http.StatusOK, map[string]any{"tokens": tokens})
}

// CreateTokenHandler mints a new named token for the authenticated user.
func (h *Handler) CreateTokenHandler(e *core.RequestEvent) error {
	user := e.Auth
	if user == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}

	req := &model.CreateTokenRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	token, err := h.service.Tokens().Create(user.Id, req.Name)
	if err != nil {
		if errors.Is(err, service.ErrTokenNameTaken) {
			return h.NewErrorResponse(e, http.StatusConflict, err.Error())
		}
		return h.NewErrorResponse(e, http.StatusBadRequest, err.Error())
	}

	return h.NewSuccessResponse(e, http.StatusCreated, token)
}

// VerifyTokenHandler validates a raw CLI token and returns the owning account.
// It is public (the token itself is the credential) and backs the CLI's
// `goport auth <token>` command so an invalid token is rejected instead of
// being silently accepted.
func (h *Handler) VerifyTokenHandler(e *core.RequestEvent) error {
	req := &model.VerifyTokenRequest{}
	if err := e.BindBody(req); err != nil {
		return h.NewErrorResponse(e, http.StatusBadRequest, "invalid request body")
	}

	owner, err := h.service.Tokens().Verify(req.Token)
	if err != nil {
		if errors.Is(err, service.ErrInvalidToken) {
			return h.NewErrorResponse(e, http.StatusUnauthorized, "Invalid token. Check the token from your GoPort dashboard.")
		}
		h.logger.Error("failed to verify token", "error", err)
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't verify the token. Please try again.")
	}

	return h.NewSuccessResponse(e, http.StatusOK, map[string]any{
		"valid": true,
		"email": owner.Email,
		"name":  owner.Name,
	})
}

// DeleteTokenHandler removes one of the authenticated user's tokens.
func (h *Handler) DeleteTokenHandler(e *core.RequestEvent) error {
	user := e.Auth
	if user == nil {
		return h.NewErrorResponse(e, http.StatusUnauthorized, "authentication required")
	}

	id := e.Request.PathValue("id")
	if id == "" {
		return h.NewErrorResponse(e, http.StatusBadRequest, "token id is required")
	}

	if err := h.service.Tokens().Delete(user.Id, id); err != nil {
		if errors.Is(err, service.ErrTokenNotFound) {
			return h.NewErrorResponse(e, http.StatusNotFound, "token not found")
		}
		h.logger.Error("failed to delete token", "error", err, "userId", user.Id, "tokenId", id)
		return h.NewErrorResponse(e, http.StatusInternalServerError, "Couldn't delete the token.")
	}

	return h.NewSuccessResponse(e, http.StatusOK, map[string]string{"message": "Token deleted"})
}
