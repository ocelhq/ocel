package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
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

func (e *Engine) FireCron(ctx context.Context, task string, scheduled time.Time) error {
	deployed, found := e.taskConsumer(task)
	if !found {
		return fmt.Errorf("no task named %q is deployed to run on its schedule", task)
	}
	stamp := scheduled.UTC().Truncate(time.Minute).Format(time.RFC3339)
	payload, err := json.Marshal(map[string]string{"timestamp": stamp})
	if err != nil {
		return err
	}
	_, err = e.Tasks().trigger(ctx, deployed, payload, &taskv1.TriggerOptions{IdempotencyKey: "cron-" + stamp})
	return err
}
