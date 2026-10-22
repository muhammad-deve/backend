package hook

import (
	"github.com/pocketbase/pocketbase/core"
	"gitlab.yurtal.tech/company/pocketbase-app-template/internal/model"
)

// shareOrphanSweepJob is the PocketBase cron job that removes stored share
// files no share row refers to.
const shareOrphanSweepJob = "goportShareOrphans"

// registerShareHooks keeps share storage in step with the shares collection.
func (h *Hook) registerShareHooks(app core.App) {
	// Runs after every committed delete of a share row, including rows
	// removed along with their owner's account (PocketBase fires record
	// hooks for cascade deletes) and rows deleted in the admin UI.
	app.OnRecordAfterDeleteSuccess(model.SharesCollection).BindFunc(func(e *core.RecordEvent) error {
		h.service.Shares().RemoveStoredFile(e.Record.GetString("storage_key"))
		return e.Next()
	})

	// Hourly, catches files a crash or an older version left behind.
	if err := app.Cron().Add(shareOrphanSweepJob, "23 * * * *", func() {
		removed, err := h.service.Shares().SweepOrphans()
		switch {
		case err != nil:
			h.logger.Error("share orphan sweep failed", "error", err)
		case removed > 0:
			h.logger.Info("share orphan sweep removed files", "count", removed)
		}
	}); err != nil {
		h.logger.Error("couldn't schedule the share orphan sweep", "error", err)
	}
}
