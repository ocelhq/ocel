package terminal

import "time"

func EpochDate(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02")
}

func EpochDateTime(sec int64) string {
	if sec == 0 {
		return "—"
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}
