package hook

import "github.com/pocketbase/pocketbase/core"

func (h *Hook) otpRequest(e *core.RecordCreateOTPRequestEvent) error {
	return h.service.Authorization().OtpRequest(e)
}

func (h *Hook) passwordResetRequest(e *core.RecordRequestPasswordResetRequestEvent) error {
	return h.service.Authorization().ResetPasswordRequest(e)
}

func (h *Hook) bookRatingCalculate(e *core.RecordEvent) error {
	return h.service.Authorization().BookRatingCalculate(e)

}

func (h *Hook) bookRatingUpdate(e *core.RecordEvent) error {
	return h.service.Authorization().BookRatingUpdate(e)

}
