package refreshquota

import (
	"fmt"
	"time"
)

// NextOccurrence returns the next valid civil-time occurrence strictly after now.
// Non-existent DST wall times are skipped; repeated wall times use one occurrence key.
func NextOccurrence(now time.Time, location *time.Location, times []DailyTime) (time.Time, string, bool) {
	if location == nil || len(times) == 0 {
		return time.Time{}, "", false
	}
	localNow := now.In(location)
	for dayOffset := 0; dayOffset < 370; dayOffset++ {
		day := localNow.AddDate(0, 0, dayOffset)
		var best time.Time
		bestLabel := ""
		for _, item := range times {
			candidate := time.Date(day.Year(), day.Month(), day.Day(), item.Hour, item.Minute, item.Second, 0, location)
			wall := candidate.In(location)
			if wall.Year() != day.Year() || wall.Month() != day.Month() || wall.Day() != day.Day() ||
				wall.Hour() != item.Hour || wall.Minute() != item.Minute || wall.Second() != item.Second {
				continue
			}
			if candidate.After(now) && (best.IsZero() || candidate.Before(best)) {
				best = candidate
				bestLabel = item.Text
			}
		}
		if !best.IsZero() {
			return best, bestLabel, true
		}
	}
	return time.Time{}, "", false
}

func OccurrenceID(at time.Time, location *time.Location, label string) string {
	if location == nil {
		location = time.Local
	}
	local := at.In(location)
	return fmt.Sprintf("%04d-%02d-%02d|%s|%s", local.Year(), local.Month(), local.Day(), label, location.String())
}
