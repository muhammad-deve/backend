package hook

import (
	"log/slog"

	"github.com/pocketbase/pocketbase"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/service"
)

type Hook struct {
	logger  *slog.Logger
	service service.I
}

func (h *Hook) Register(app *pocketbase.PocketBase) {
}

func New(logger *slog.Logger, service service.I) *Hook {
	return &Hook{
		logger:  logger,
		service: service,
	}
}
