package hook

import (
	"github.com/pocketbase/pocketbase"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/internal/service"
	"log/slog"
)

type Hook struct {
	logger  *slog.Logger
	service service.I
}

func (h *Hook) Register(app *pocketbase.PocketBase) {
	//app.OnRecordRequestOTPRequest(model.UsersCollection).BindFunc(recordRequestOTPRequestEventWrapper(h.otpRequest))
	//app.OnRecordRequestPasswordResetRequest(model.UsersCollection).BindFunc(recordRequestPasswordResetRequestEventWrapper(h.passwordResetRequest))
	//
	//app.OnMailerRecordPasswordResetSend().BindFunc(mailerRecordPasswordResetSendEventWrapper)
	//app.OnMailerRecordOTPSend().BindFunc(mailerRecordOTPSendEventWrapper)
}

func New(logger *slog.Logger, service service.I) *Hook {
	return &Hook{
		logger:  logger,
		service: service,
	}
}
