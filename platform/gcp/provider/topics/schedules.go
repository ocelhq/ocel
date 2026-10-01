package topics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	cloudscheduler "google.golang.org/api/cloudscheduler/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const scheduleTimeZone = "Etc/UTC"

func (t Topology) jobPath(clients *ports.Clients, task string) string {
	return "projects/" + clients.Project + "/locations/" + clients.Region + "/jobs/" + t.Names.ScheduleJob(task)
}

func (t Topology) ensureSchedule(ctx context.Context, clients *ports.Clients, task, cron string) error {
	service, err := clients.Scheduler()
	if err != nil {
		return err
	}
	path := t.jobPath(clients, task)
	desired := &cloudscheduler.Job{
		Name:     path,
		Schedule: cron,
		TimeZone: scheduleTimeZone,
		PubsubTarget: &cloudscheduler.PubsubTarget{
			TopicName:  topicPath(clients.Project, t.Names.Topic(task)),
			Data:       base64Of([]byte("{}")),
			Attributes: map[string]string{ScheduleAttribute: "true"},
		},
	}
	parent := "projects/" + clients.Project + "/locations/" + clients.Region
	err = retried(ctx, func() error {
		_, err := service.Projects.Locations.Jobs.Create(parent, desired).Context(ctx).Do()
		return err
	})
	if err == nil {
		return nil
	}
	if !isAnswered(err, http.StatusConflict) {
		return fmt.Errorf("create Cloud Scheduler job %s: %w", path, err)
	}
	desired.Name = ""
	err = retried(ctx, func() error {
		_, err := service.Projects.Locations.Jobs.Patch(path, desired).UpdateMask("schedule,timeZone,pubsubTarget").Context(ctx).Do()
		return err
	})
	if err != nil {
		return fmt.Errorf("update Cloud Scheduler job %s: %w", path, err)
	}
	return nil
}

func (t Topology) removeSchedule(ctx context.Context, clients *ports.Clients, task string) error {
	service, err := clients.Scheduler()
	if err != nil {
		return err
	}
	path := t.jobPath(clients, task)
	if err := retried(ctx, func() error {
		_, err := service.Projects.Locations.Jobs.Delete(path).Context(ctx).Do()
		return err
	}); err != nil && !isAnswered(err, http.StatusNotFound) {
		return fmt.Errorf("delete Cloud Scheduler job %s: %w", path, err)
	}
	return nil
}

type scheduledPayload struct {
	Timestamp string `json:"timestamp"`
}

func scheduledPayloadOf(fired time.Time) json.RawMessage {
	payload, _ := json.Marshal(scheduledPayload{Timestamp: fired.UTC().Truncate(time.Minute).Format(time.RFC3339)})
	return payload
}
