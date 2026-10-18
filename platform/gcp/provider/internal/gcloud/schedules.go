package gcloud

import (
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	cloudscheduler "google.golang.org/api/cloudscheduler/v1"
)

const (
	ScheduleTimeZone   = "Etc/UTC"
	scheduleUpdateMask = "description,schedule,timeZone,httpTarget,retryConfig"

	ScheduleEnabled  = "ENABLED"
	SchedulePaused   = "PAUSED"
	scheduleDisabled = "DISABLED"
)

func SchedulePath(c *Clients, name string) string { return c.Location() + "/jobs/" + name }

func PutSchedule(ctx context.Context, c *Clients, name string, desired *cloudscheduler.Job) error {
	service, err := c.Scheduler()
	if err != nil {
		return err
	}
	path := SchedulePath(c, name)
	create := func() (*cloudscheduler.Job, error) {
		return googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Create(c.Location(), desired).Context(ctx).Do)
	}
	job, err := create()
	if Taken(err) {
		patched := *desired
		patched.Name = ""
		job, err = googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Patch(path, &patched).
			UpdateMask(scheduleUpdateMask).Context(ctx).Do)
	}
	if err != nil {
		return fmt.Errorf("create the Cloud Scheduler job %s: %w", name, err)
	}
	switch job.State {
	case SchedulePaused:
		if _, err := googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Resume(path, &cloudscheduler.ResumeJobRequest{}).Context(ctx).Do); err != nil {
			return fmt.Errorf("resume the paused Cloud Scheduler job %s: %w", name, err)
		}
	case scheduleDisabled:
		if err := TakeSchedule(ctx, c, name); err != nil {
			return err
		}
		if _, err := create(); err != nil {
			return fmt.Errorf("create the Cloud Scheduler job %s again in place of the one Cloud Scheduler disabled: %w", name, err)
		}
	}
	return nil
}

func TakeSchedule(ctx context.Context, c *Clients, name string) error {
	service, err := c.Scheduler()
	if err != nil {
		return err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.Locations.Jobs.Delete(SchedulePath(c, name)).Context(ctx).Do); err != nil && !Absent(err) {
		return fmt.Errorf("delete the Cloud Scheduler job %s: %w", name, err)
	}
	return nil
}

func EveryStep(steps ...func() error) error {
	errs := make([]error, 0, len(steps))
	for _, step := range steps {
		errs = append(errs, step())
	}
	return errors.Join(errs...)
}
