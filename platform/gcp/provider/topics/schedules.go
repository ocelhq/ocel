package topics

import (
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"

	cloudscheduler "google.golang.org/api/cloudscheduler/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const scheduleTimeZone = "Etc/UTC"

func (t Topology) jobPath(clients *ports.Clients, task string) string {
	return "projects/" + clients.Project + "/locations/" + clients.Region + "/jobs/" + t.Names.ScheduleJob(task)
}

func (t Topology) buildJob(clients *ports.Clients, task, cron string) *cloudscheduler.Job {
	return &cloudscheduler.Job{
		Name:     t.jobPath(clients, task),
		Schedule: cron,
		TimeZone: scheduleTimeZone,
		PubsubTarget: &cloudscheduler.PubsubTarget{
			TopicName:  TopicPath(clients.Project, t.Names.Topic(task)),
			Data:       base64.StdEncoding.EncodeToString([]byte("{}")),
			Attributes: map[string]string{ScheduleAttribute: "true"},
		},
	}
}

func isSameTarget(current, desired *cloudscheduler.Job) bool {
	if current.PubsubTarget == nil {
		return false
	}
	return current.TimeZone == desired.TimeZone &&
		current.PubsubTarget.TopicName == desired.PubsubTarget.TopicName &&
		current.PubsubTarget.Data == desired.PubsubTarget.Data &&
		maps.Equal(current.PubsubTarget.Attributes, desired.PubsubTarget.Attributes)
}

func (t Topology) readJob(ctx context.Context, clients *ports.Clients, task string) (*cloudscheduler.Job, error) {
	service, err := clients.Scheduler()
	if err != nil {
		return nil, err
	}
	path := t.jobPath(clients, task)
	job, err := googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Get(path).Context(ctx).Do)
	if isAnswered(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Cloud Scheduler job %s: %w", path, err)
	}
	return job, nil
}

func (t Topology) readScheduleDifference(ctx context.Context, clients *ports.Clients, task, cron string) (string, error) {
	current, err := t.readJob(ctx, clients, task)
	if err != nil {
		return "", err
	}
	desired := t.buildJob(clients, task, cron)
	switch {
	case current == nil:
		return fmt.Sprintf("Cloud Scheduler job %s is created to fire at %q", desired.Name, cron), nil
	case current.Schedule != desired.Schedule:
		return fmt.Sprintf("Cloud Scheduler job %s changes from firing at %q to %q", desired.Name, current.Schedule, desired.Schedule), nil
	case !isSameTarget(current, desired):
		return fmt.Sprintf("Cloud Scheduler job %s has its time zone and Pub/Sub target rewritten", desired.Name), nil
	}
	return "", nil
}

func (t Topology) readDroppedScheduleDifference(ctx context.Context, clients *ports.Clients, task string) (string, error) {
	current, err := t.readJob(ctx, clients, task)
	if err != nil || current == nil {
		return "", err
	}
	return fmt.Sprintf("%s no longer declares a cron, so Cloud Scheduler job %s is deleted", task, t.jobPath(clients, task)), nil
}

func (t Topology) ensureSchedule(ctx context.Context, clients *ports.Clients, task, cron string) error {
	service, err := clients.Scheduler()
	if err != nil {
		return err
	}
	desired := t.buildJob(clients, task, cron)
	path := desired.Name
	parent := "projects/" + clients.Project + "/locations/" + clients.Region
	err = googleretry.Done(ctx, func() error {
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
	err = googleretry.Done(ctx, func() error {
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
	if err := googleretry.Done(ctx, func() error {
		_, err := service.Projects.Locations.Jobs.Delete(path).Context(ctx).Do()
		return err
	}); err != nil && !isAnswered(err, http.StatusNotFound) {
		return fmt.Errorf("delete Cloud Scheduler job %s: %w", path, err)
	}
	return nil
}
