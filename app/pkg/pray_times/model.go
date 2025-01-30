package pray_times

import "time"

type PrayTimes struct {
	Date     string `json:"date"`
	Imsak    string `json:"imsak"`
	Fajr     string `json:"fajr"`
	Sunrise  string `json:"sunrise"`
	Dhuhr    string `json:"dhuhr"`
	Asr      string `json:"asr"`
	Maghrib  string `json:"maghrib"`
	Isha     string `json:"isha"`
	Midnight string `json:"midnight"`
}

type GetByLocationDayRequest struct {
	Date time.Time `json:"day"`
	Lat  float64   `json:"lat"`
	Lon  float64   `json:"lon"`
}
