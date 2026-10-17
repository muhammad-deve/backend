package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// blocked_words lists words a custom subdomain may not contain. Add rows in
// the PocketBase admin; registration reads the table each time, so changes
// apply at once. With whole_word off a word matches anywhere in the name
// ("spam" blocks "my-spam-app" and "spammer"); turn it on for short words that
// hide inside harmless ones, so "ass" blocks "ass" and "my-ass" but not
// "class". Matching ignores case and hyphens and reads common digit swaps
// (0→o, 1→i or l, 3→e, 4→a, 5→s, 7→t, 8→b) as letters.
const blockedWordsCollectionID = "pbc_3311000201"

func init() {
	m.Register(func(app core.App) error {
		collection := core.NewBaseCollection("blocked_words", blockedWordsCollectionID)
		// Rules stay nil: only superusers can read or change the list.
		collection.Fields.Add(&core.TextField{
			Id:       "text3311000202",
			Name:     "word",
			Required: true,
			Min:      2,
			Max:      63,
		})
		collection.Fields.Add(&core.BoolField{
			Id:   "bool3311000203",
			Name: "whole_word",
		})
		collection.Fields.Add(&core.AutodateField{
			Id:       "autodate3311000204",
			Name:     "created",
			OnCreate: true,
		})
		collection.AddIndex("idx_blocked_words_word", true, "word", "")
		return app.Save(collection)
	}, func(app core.App) error {
		collection, err := app.FindCollectionByNameOrId(blockedWordsCollectionID)
		if err != nil {
			return err
		}
		return app.Delete(collection)
	})
}
