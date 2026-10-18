package tasks

import (
	"encoding/json"
	"time"
)

type cronMarker struct {
	Cron          string `json:"ocelCron"`
	ScheduledTime string `json:"scheduledTime"`
}

func readCronMarker(body string) (cronMarker, bool) {
	var marker cronMarker
	if err := json.Unmarshal([]byte(body), &marker); err != nil || marker.Cron == "" {
		return cronMarker{}, false
	}
	return marker, true
}

func (m cronMarker) scheduledAt() time.Time {
	scheduled, err := time.Parse(time.RFC3339, m.ScheduledTime)
	if err != nil {
		return time.Now()
	}
	return scheduled
}
