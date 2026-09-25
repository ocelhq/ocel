package gcp_test

import (
	"context"
	"testing"

	"google.golang.org/api/cloudscheduler/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func schedules(t *testing.T) *cloudscheduler.Service {
	t.Helper()
	service, err := cloudscheduler.NewService(context.Background(), ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the Cloud Scheduler API: %v", err)
	}
	return service
}

func TestLiveTheClassStandsItsEnvSyncerAndRemovingItTakesTheSyncerWithIt(t *testing.T) {
	p := live(t)
	servicesEnabled(t)
	class := providerkit.ClassPreview
	held := names(t, p)
	ctx := context.Background()

	bootstrapper := bootstrapperOf(t, p)
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", class, err)
	}
	schedule := "projects/" + liveProject() + "/locations/" + liveRegion() + "/jobs/" + held.EnvSyncJob(class)
	stood, err := schedules(t).Projects.Locations.Jobs.Get(schedule).Context(ctx).Do()
	if err != nil {
		t.Fatalf("Get(%s) after a bootstrap = %v, want the schedule that starts the syncer", schedule, err)
	}
	if stood.Schedule != "* * * * *" {
		t.Errorf("the syncer is started on %q, want every minute", stood.Schedule)
	}
	accountPaths := []string{
		"projects/" + liveProject() + "/serviceAccounts/" + held.EnvSyncAccountEmail(class),
		"projects/" + liveProject() + "/serviceAccounts/" + held.EnvSyncInvokerEmail(class),
	}
	for _, path := range accountPaths {
		if _, err := accounts(t).Projects.ServiceAccounts.Get(path).Context(ctx).Do(); err != nil {
			t.Errorf("Get(%s) after a bootstrap = %v, want the account", path, err)
		}
	}
	described, err := bootstrapper.Describe(ctx, class)
	if err != nil || len(described.Stacks) != 1 || !described.Stacks[0].DigestCurrent {
		t.Errorf("Describe() = %+v, %v, want the stack current with the syncer standing", described, err)
	}

	if err := bootstrapper.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", class, err)
	}
	if _, err := schedules(t).Projects.Locations.Jobs.Get(schedule).Context(ctx).Do(); err == nil {
		t.Errorf("%s still stands after the class was removed, and it would start a syncer nothing runs", schedule)
	}
	for _, path := range accountPaths {
		if _, err := accounts(t).Projects.ServiceAccounts.Get(path).Context(ctx).Do(); err == nil {
			t.Errorf("%s still stands after the class was removed", path)
		}
	}
}
