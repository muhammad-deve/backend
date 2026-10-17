package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Each token is one device, so the device's details live on its token row:
// which GoPort version and OS it runs, how it was installed, the country it
// connects from (looked up on the server; the IP itself is not stored), and
// how often its tunnels have had to reconnect and why.
var deviceInfoFields = []struct {
	id, json string
}{
	{"text3311000101", `{"id":"text3311000101","name":"client_version","type":"text","max":32,"min":0,"pattern":"","hidden":false,"presentable":false,"primaryKey":false,"required":false,"system":false}`},
	{"text3311000102", `{"id":"text3311000102","name":"client_os","type":"text","max":32,"min":0,"pattern":"","hidden":false,"presentable":false,"primaryKey":false,"required":false,"system":false}`},
	{"text3311000103", `{"id":"text3311000103","name":"client_arch","type":"text","max":32,"min":0,"pattern":"","hidden":false,"presentable":false,"primaryKey":false,"required":false,"system":false}`},
	{"text3311000104", `{"id":"text3311000104","name":"install_method","type":"text","max":32,"min":0,"pattern":"","hidden":false,"presentable":false,"primaryKey":false,"required":false,"system":false}`},
	{"text3311000105", `{"id":"text3311000105","name":"country","type":"text","max":2,"min":0,"pattern":"","hidden":false,"presentable":false,"primaryKey":false,"required":false,"system":false}`},
	{"number3311000106", `{"id":"number3311000106","name":"reconnects","type":"number","max":null,"min":0,"onlyInt":true,"hidden":false,"presentable":false,"required":false,"system":false}`},
	{"text3311000107", `{"id":"text3311000107","name":"last_disconnect","type":"text","max":32,"min":0,"pattern":"","hidden":false,"presentable":false,"primaryKey":false,"required":false,"system":false}`},
}

func init() {
	m.Register(func(app core.App) error {
		tokens, err := app.FindCollectionByNameOrId(tokensCollectionID)
		if err != nil {
			return err
		}
		for _, field := range deviceInfoFields {
			if err := tokens.Fields.AddMarshaledJSON([]byte(field.json)); err != nil {
				return err
			}
		}
		return app.Save(tokens)
	}, func(app core.App) error {
		tokens, err := app.FindCollectionByNameOrId(tokensCollectionID)
		if err != nil {
			return err
		}
		for _, field := range deviceInfoFields {
			tokens.Fields.RemoveById(field.id)
		}
		return app.Save(tokens)
	})
}
