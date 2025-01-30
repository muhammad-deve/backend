package hook

import (
	"github.com/pocketbase/pocketbase/core"
	"gitlab.saidoff.uz/company/muslim-administration/reading/back/pkg/utils"
	"strings"
)

func recordRequestOTPRequestEventWrapper(fn func(e *core.RecordCreateOTPRequestEvent) error) func(e *core.RecordCreateOTPRequestEvent) error {
	return func(e *core.RecordCreateOTPRequestEvent) error {
		err := fn(e)
		if err != nil {
			return err
		}
		return e.Next()
	}
}

func recordRequestPasswordResetRequestEventWrapper(fn func(e *core.RecordRequestPasswordResetRequestEvent) error) func(e *core.RecordRequestPasswordResetRequestEvent) error {
	return func(e *core.RecordRequestPasswordResetRequestEvent) error {
		err := fn(e)
		if err != nil {
			return err
		}
		return e.Next()
	}
}

func recordConfirmPasswordResetRequestEventWrapper(fn func(e *core.RecordConfirmPasswordResetRequestEvent) error) func(e *core.RecordConfirmPasswordResetRequestEvent) error {
	return func(e *core.RecordConfirmPasswordResetRequestEvent) error {
		err := fn(e)
		if err != nil {
			return err
		}
		return e.Next()
	}
}

func mailerRecordOTPSendEventWrapper(e *core.MailerRecordEvent) error {
	email := e.Record.GetString("email")
	splitEmail := strings.Split(email, "@")
	phone := splitEmail[0]
	if utils.IsValidUzbPhoneNumber(phone) {
		return nil
	}
	return e.Next()
}

func mailerRecordPasswordResetSendEventWrapper(e *core.MailerRecordEvent) error {
	email := e.Record.GetString("email")
	splitEmail := strings.Split(email, "@")
	phone := splitEmail[0]
	if utils.IsValidUzbPhoneNumber(phone) {
		return nil
	}
	return e.Next()
}

func recordEventWrapper(fn func(*core.RecordEvent) error) func(*core.RecordEvent) error {
	return func(e *core.RecordEvent) error {
		err := fn(e)
		if err != nil {
			return err
		}
		return e.Next()
	}
}

func recordCreateRequestEventWrapper(fn func(e *core.RecordRequestEvent) error) func(e *core.RecordRequestEvent) error {
	return func(e *core.RecordRequestEvent) error {
		err := fn(e)
		if err != nil {
			return err
		}
		return e.Next()
	}
}

func recordAuthRequestEventWrapper(fn func(e *core.RecordAuthRequestEvent) error) func(e *core.RecordAuthRequestEvent) error {
	return func(e *core.RecordAuthRequestEvent) error {
		err := fn(e)
		if err != nil {
			return err
		}
		return e.Next()
	}
}

func recordViewRequestEventWrapper(fn func(e *core.RecordRequestEvent) error) func(e *core.RecordRequestEvent) error {
	return func(e *core.RecordRequestEvent) error {
		err := fn(e)
		if err != nil {
			return err
		}
		return e.Next()
	}
}

func recordsListRequestEventWrapper(fn func(e *core.RecordsListRequestEvent) error) func(e *core.RecordsListRequestEvent) error {
	return func(e *core.RecordsListRequestEvent) error {
		err := fn(e)
		if err != nil {
			return err
		}
		return e.Next()
	}
}
