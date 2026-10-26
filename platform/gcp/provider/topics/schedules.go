package topics

import (
	"context"
	"encoding/base64"
	"fmt"
	"maps"
	"net/http"
	"strings"

	cloudscheduler "google.golang.org/api/cloudscheduler/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
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

func (t Topology) patchJob(ctx context.Context, clients *ports.Clients, task string, spec *provider.TopicSpec) (surface.RecordedPatch[cloudscheduler.Job, JobUpdate], error) {
	desired, err := t.buildJob(clients, task, spec)
	if err != nil {
		return surface.RecordedPatch[cloudscheduler.Job, JobUpdate]{}, err
	}
	asked, evaluated := t.Asked[task]
	if !evaluated {
		return unpatched[cloudscheduler.Job, JobUpdate](*desired), nil
	}
	return patch(ctx, clients.Records, t.Names.jobPatchesKey(task), JobSurface, asked, *desired)
}

func (t Topology) readScheduleDifferences(ctx context.Context, clients *ports.Clients, task string, spec *provider.TopicSpec) ([]string, error) {
	current, err := t.readJob(ctx, clients, task, spec)
	if err != nil {
		return nil, err
	}
	job, err := t.patchJob(ctx, clients, task, spec)
	if err != nil {
		return nil, err
	}
	desired := &job.Desired
	cron := spec.Cron
	var differences []string
	switch {
	case current == nil:
		return []string{fmt.Sprintf("Cloud Scheduler job %s is created to fire at %q", desired.Name, cron)}, nil
	case current.Schedule != desired.Schedule:
		differences = append(differences, fmt.Sprintf("Cloud Scheduler job %s changes from firing at %q to %q", desired.Name, current.Schedule, desired.Schedule))
	case !isSameTarget(current, desired):
		differences = append(differences, fmt.Sprintf("Cloud Scheduler job %s has its time zone and Pub/Sub target rewritten", desired.Name))
	}
	patchedDifferences, err := job.Drift(current, &JobUpdate{})
	return append(differences, prefixed("the Cloud Scheduler job's ", patchedDifferences)...), err
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
	job, err := t.patchJob(ctx, clients, task, spec)
	if err != nil {
		return err
	}
	path := job.Desired.Name
	parent := "projects/" + clients.Project + "/locations/" + clients.Region
	return job.Apply(ctx, func() error {
		err := googleretry.Done(ctx, func() error {
			_, err := service.Projects.Locations.Jobs.Create(parent, &job.Desired).Context(ctx).Do()
			return err
		})
		if err == nil {
			return nil
		}
		if !isAnswered(err, http.StatusConflict) {
			return fmt.Errorf("create Cloud Scheduler job %s: %w", path, err)
		}
		return updateJob(ctx, service, path, job)
	})
}

func updateJob(ctx context.Context, service *cloudscheduler.Service, path string, job surface.RecordedPatch[cloudscheduler.Job, JobUpdate]) error {
	update := JobUpdate{Job: job.Desired, Mask: []string{"schedule", "timeZone", "pubsubTarget"}}
	update.Job.Name = ""
	if !job.IsEmpty() {
		current, err := googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Get(path).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("read Cloud Scheduler job %s: %w", path, err)
		}
		if _, err := job.Drift(current, &update); err != nil {
			return err
		}
	}
	err := googleretry.Done(ctx, func() error {
		_, err := service.Projects.Locations.Jobs.Patch(path, &update.Job).UpdateMask(strings.Join(update.Mask, ",")).Context(ctx).Do()
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
	return forgetPatches(ctx, clients, t.Names.jobPatchesKey(task))
}
