package geoip

import (
	"os"
	"testing"
)

func TestMissingDatabaseFindsNothing(t *testing.T) {
	db := Open(t.TempDir() + "/missing.mmdb")
	if db != nil {
		t.Fatal("Open should return nil when the file is missing")
	}
	if got := db.Country("8.8.8.8"); got != "" {
		t.Fatalf("nil DB returned %q", got)
	}
}

// Runs only with a real database: GEOIP_TEST_DB=/path/to/country.mmdb.
func TestCountryLookup(t *testing.T) {
	path := os.Getenv("GEOIP_TEST_DB")
	if path == "" {
		t.Skip("set GEOIP_TEST_DB to run")
	}
	db := Open(path)
	if db == nil {
		t.Fatal("could not open the database")
	}
	cases := map[string]string{
		"8.8.8.8:443":     "US",
		"213.230.64.1":    "UZ", // Uztelecom
		"127.0.0.1:52614": "",
		"not an ip":       "",
	}
	for addr, want := range cases {
		if got := db.Country(addr); got != want {
			t.Errorf("Country(%q) = %q, want %q", addr, got, want)
		}
	}
}
