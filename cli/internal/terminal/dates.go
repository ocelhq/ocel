package terminal

import (
	"fmt"
	"time"
)

const (
	dateLayout = "2006-01-02"
	ageLimit   = 30 * 24 * time.Hour
)

func EpochDate(sec int64) string {
	if sec == 0 {
		return NoValue
	}
	return time.Unix(sec, 0).UTC().Format(dateLayout)
}

func EpochDateTime(sec int64) string {
	if sec == 0 {
		return NoValue
	}
	return time.Unix(sec, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}

func (p Presentation) EpochAge(sec int64) string {
	if sec == 0 || !p.TTY {
		return EpochDate(sec)
	}
	return Age(time.Unix(sec, 0), time.Now())
}

func Age(at, now time.Time) string {
	elapsed := now.Sub(at)
	switch {
	case elapsed < 0 || elapsed >= ageLimit:
		return at.UTC().Format(dateLayout)
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed/time.Minute))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(elapsed/(24*time.Hour)))
}

func EpochRFC3339(sec int64) string {
	if sec == 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func FormatRFC3339(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
