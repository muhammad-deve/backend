// Package geoip turns an IP address into a two-letter country code using a
// local MMDB country database (the Docker image downloads DB-IP's free
// "IP to Country Lite"). Lookups never leave the server.
package geoip

import (
	"log"
	"net"
	"os"
	"strings"

	"github.com/oschwald/maxminddb-golang"
)

// DefaultPath is where the Docker image puts the database.
const DefaultPath = "/app/geo/country.mmdb"

// DB is a country lookup. A nil *DB is valid and finds nothing, so the
// service keeps working when the database is missing.
type DB struct {
	reader *maxminddb.Reader
}

// Open loads the database at path, or at $GEOIP_DB, or at DefaultPath. When
// none is found it logs once and returns nil.
func Open(path string) *DB {
	if path == "" {
		path = os.Getenv("GEOIP_DB")
	}
	if path == "" {
		path = DefaultPath
	}
	reader, err := maxminddb.Open(path)
	if err != nil {
		log.Printf("geoip: no country database at %s (%v); countries will be left empty", path, err)
		return nil
	}
	return &DB{reader: reader}
}

// Country returns the ISO 3166 code for addr ("UZ"), or "" when unknown.
// addr may be "ip" or "ip:port".
func (db *DB) Country(addr string) string {
	if db == nil || db.reader == nil {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	ip := net.ParseIP(strings.TrimSpace(addr))
	if ip == nil {
		return ""
	}
	var record struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := db.reader.Lookup(ip, &record); err != nil {
		return ""
	}
	return strings.ToUpper(record.Country.ISOCode)
}
