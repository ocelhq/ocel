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

func EpochRFC3339(sec int64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func NormalizeRFC3339(raw string) string {
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return ""
	}
	return FormatRFC3339(&at)
}

func FormatRFC3339(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
