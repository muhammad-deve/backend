package pray_times

import (
	"time"
)

func GetPrayerTimesDayTashkent(r GetByLocationDayRequest) PrayTimes {
	loc := time.FixedZone("Asia/Tashkent", 5*3600) // 5 hours offset from UTC
	// Get the current time in the specified location
	now := time.Now().In(loc)

	// Get the timezone offset in seconds
	_, offset := now.Zone()

	// Convert the offset to hours
	timezone := offset / 3600

	SetMethod("Karachi")
	adjustment := map[string]interface{}{
		"fajr":    15.45,
		"dhuhr":   5,
		"asr":     "Hanafi",
		"maghrib": 0.833,
		"isha":    15,
	}
	Adjust(adjustment)
	pt := GetTimes(r.Date, []float64{r.Lat, r.Lon}, timezone, 0, "")

	return PrayTimes{
		Date:     r.Date.Format("02-01-2006"),
		Imsak:    pt["imsak"],
		Fajr:     pt["fajr"],
		Sunrise:  pt["sunrise"],
		Dhuhr:    pt["dhuhr"],
		Asr:      pt["asr"],
		Maghrib:  pt["maghrib"],
		Isha:     pt["isha"],
		Midnight: pt["midnight"],
	}
}
