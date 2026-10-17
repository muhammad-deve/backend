package service

import (
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

func tunnelRow(t *testing.T, isCustom bool, created time.Time) *core.Record {
	t.Helper()
	collection := core.NewBaseCollection("tunnels")
	collection.Fields.Add(&core.BoolField{Name: "is_custom"}, &core.AutodateField{Name: "created", OnCreate: true})
	record := core.NewRecord(collection)
	record.Set("is_custom", isCustom)
	created_, err := types.ParseDateTime(created)
	if err != nil {
		t.Fatal(err)
	}
	record.SetRaw("created", created_)
	return record
}

func TestGrandfatheredCustomNames(t *testing.T) {
	before := customProOnlySince.Add(-24 * time.Hour)
	after := customProOnlySince.Add(24 * time.Hour)

	cases := []struct {
		name          string
		isCustom      bool
		created       time.Time
		grandfathered bool
		needsPro      bool
	}{
		{"custom name from before the Pro change", true, before, true, false},
		{"custom name claimed after it", true, after, false, true},
		{"generated name, old", false, before, false, false},
		{"generated name, new", false, after, false, false},
	}
	for _, c := range cases {
		record := tunnelRow(t, c.isCustom, c.created)
		if got := isGrandfathered(record); got != c.grandfathered {
			t.Errorf("%s: isGrandfathered = %v, want %v", c.name, got, c.grandfathered)
		}
		if got := needsPro(record); got != c.needsPro {
			t.Errorf("%s: needsPro = %v, want %v", c.name, got, c.needsPro)
		}
	}
}
