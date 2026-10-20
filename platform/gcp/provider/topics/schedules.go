package topics

import (
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"

	cloudscheduler "google.golang.org/api/cloudscheduler/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const scheduleTimeZone = "Etc/UTC"

func (t Topology) jobPath(clients *ports.Clients, task string) (string, error) {
	job, err := t.Names.Topic(task)
	if err != nil {
		return "", err
	}
	return "projects/" + clients.Project + "/locations/" + clients.Region + "/jobs/" + job, nil
}

func (t Topology) buildJob(clients *ports.Clients, task string, spec *provider.TopicSpec) (*cloudscheduler.Job, error) {
	path, err := t.jobPath(clients, task)
	if err != nil {
		return nil, err
	}
	topic, err := t.Names.topicPath(clients.Project, task)
	if err != nil {
		return nil, err
	}
	return &cloudscheduler.Job{
		Name:     path,
		Schedule: spec.Cron,
		TimeZone: scheduleTimeZone,
		PubsubTarget: &cloudscheduler.PubsubTarget{
			TopicName:  topic,
			Data:       base64.StdEncoding.EncodeToString([]byte("{}")),
			Attributes: map[string]string{ScheduleAttribute: "true"},
		},
	}, nil
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

func (t Topology) readJob(ctx context.Context, clients *ports.Clients, task string, spec *provider.TopicSpec) (*cloudscheduler.Job, error) {
	service, err := clients.Scheduler()
	if err != nil {
		return nil, err
	}
	path, err := t.jobPath(clients, task)
	if err != nil {
		return nil, err
	}
	job, err := googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Get(path).Context(ctx).Do)
	if isAnswered(err, http.StatusNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Cloud Scheduler job %s: %w", path, err)
	}
	return job, nil
}

func (t Topology) readScheduleDifference(ctx context.Context, clients *ports.Clients, task string, spec *provider.TopicSpec) (string, error) {
	current, err := t.readJob(ctx, clients, task, spec)
	if err != nil {
		return "", err
	}
	desired, err := t.buildJob(clients, task, spec)
	if err != nil {
		return "", err
	}
	cron := spec.Cron
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

func (t Topology) readDroppedScheduleDifference(ctx context.Context, clients *ports.Clients, task string, before *provider.TopicSpec) (string, error) {
	current, err := t.readJob(ctx, clients, task, before)
	if err != nil || current == nil {
		return "", err
	}
	return fmt.Sprintf("%s no longer declares a cron, so Cloud Scheduler job %s is deleted", task, current.Name), nil
}

func (t Topology) ensureSchedule(ctx context.Context, clients *ports.Clients, task string, spec *provider.TopicSpec) error {
	service, err := clients.Scheduler()
	if err != nil {
		return err
	}
	desired, err := t.buildJob(clients, task, spec)
	if err != nil {
		return err
	}
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

func (t Topology) removeSchedule(ctx context.Context, clients *ports.Clients, task string, spec *provider.TopicSpec) error {
	service, err := clients.Scheduler()
	if err != nil {
		return err
	}
	path, err := t.jobPath(clients, task)
	if err != nil {
		return err
	}
	if err := googleretry.Done(ctx, func() error {
		_, err := service.Projects.Locations.Jobs.Delete(path).Context(ctx).Do()
		return err
	}); err != nil && !isAnswered(err, http.StatusNotFound) {
		return fmt.Errorf("delete Cloud Scheduler job %s: %w", path, err)
	}
	return nil
}
