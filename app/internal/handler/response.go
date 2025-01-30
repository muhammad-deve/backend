package handler

import (
	"github.com/pocketbase/pocketbase/core"
)

func (h *Handler) NewErrorResponse(e *core.RequestEvent, statusCode int, err string) error {
	return e.JSON(statusCode, map[string]string{"error": err})
}

func (h *Handler) NewSuccessResponse(e *core.RequestEvent, statusCode int, content interface{}) error {
	return e.JSON(statusCode, content)
}
