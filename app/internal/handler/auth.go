package handler

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

func (h *Handler) AuthHandler(e *core.RequestEvent) error {
	return e.JSON(http.StatusOK, nil)
}
