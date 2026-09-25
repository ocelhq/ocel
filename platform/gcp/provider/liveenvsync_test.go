package gcp_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"google.golang.org/api/cloudscheduler/v1"
	run "google.golang.org/api/run/v2"

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

func cloudRun(t *testing.T) *run.Service {
	t.Helper()
	service, err := run.NewService(context.Background(), ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatalf("reach the Cloud Run API: %v", err)
	}
	return service
}

func polled(t *testing.T, uri string) int {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, uri, nil)
		if err != nil {
			t.Fatal(err)
		}
		status := 0
		if resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req); err == nil {
			status = resp.StatusCode
			_ = resp.Body.Close()
		}
		if status == http.StatusNoContent || time.Now().After(deadline) {
			return status
		}
		time.Sleep(time.Second)
	}
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
	location := "projects/" + liveProject() + "/locations/" + liveRegion()
	servicePath := location + "/services/" + held.EnvSync(class)
	service, err := cloudRun(t).Projects.Locations.Services.Get(servicePath).Context(ctx).Do()
	if err != nil {
		t.Fatalf("Get(%s) after a bootstrap = %v, want the service the syncer answers on", servicePath, err)
	}
	if service.Ingress != "INGRESS_TRAFFIC_INTERNAL_ONLY" || service.InvokerIamDisabled {
		t.Errorf("the syncer takes %s with invokerIamDisabled %t, want internal traffic from a caller holding run.invoker", service.Ingress, service.InvokerIamDisabled)
	}
	container := service.Template.Containers[0]
	if !container.Resources.CpuIdle || container.Resources.Limits["cpu"] != "0.08" || service.Template.Scaling.MaxInstanceCount != 1 {
		t.Errorf("the syncer runs %+v scaled %+v, want request-based billing on 0.08 vCPU and one instance at most", container.Resources, service.Template.Scaling)
	}
	policy, err := cloudRun(t).Projects.Locations.Services.GetIamPolicy(servicePath).Context(ctx).Do()
	if err != nil {
		t.Fatalf("GetIamPolicy(%s) = %v", servicePath, err)
	}
	invoker := "serviceAccount:" + held.EnvSyncInvokerEmail(class)
	if !slices.ContainsFunc(policy.Bindings, func(binding *run.GoogleIamV1Binding) bool {
		return binding.Role == "roles/run.invoker" && slices.Equal(binding.Members, []string{invoker})
	}) {
		t.Errorf("the syncer's policy binds %+v, want %s alone holding roles/run.invoker", policy.Bindings, invoker)
	}

	schedule := location + "/jobs/" + held.EnvSync(class)
	stood, err := schedules(t).Projects.Locations.Jobs.Get(schedule).Context(ctx).Do()
	if err != nil {
		t.Fatalf("Get(%s) after a bootstrap = %v, want the schedule that calls the syncer", schedule, err)
	}
	if stood.Schedule != "* * * * *" || stood.HttpTarget == nil || stood.HttpTarget.Uri != service.Uri+"/" || stood.HttpTarget.HttpMethod != http.MethodPost {
		t.Errorf("the syncer is called %+v on %q, want POST %s/ every minute", stood.HttpTarget, stood.Schedule, service.Uri)
	}
	if emulated() {
		if status := polled(t, reachable(t, service.Uri)+"/"); status != http.StatusNoContent {
			t.Errorf("POST / on the syncer = %d, want 204: a poll answers 204 even when it fails, so Cloud Scheduler adds no retry to the syncer's own backoff", status)
		}
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
	plan, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class})
	if err != nil {
		t.Fatalf("Plan() after a bootstrap = %v", err)
	}
	rows := rowsOf(t, plan)
	for _, id := range []string{"run:service/" + held.EnvSync(class), "cloudscheduler:job/" + held.EnvSync(class)} {
		if row := rows[id]; row.Action != providerkit.ActionKeep {
			t.Errorf("Plan() after a bootstrap shows %s as %q (%q), want it kept: what the bootstrap stood reads as what it stands", id, row.Action, row.Reason)
		}
	}

	if err := bootstrapper.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", class, err)
	}
	if _, err := schedules(t).Projects.Locations.Jobs.Get(schedule).Context(ctx).Do(); err == nil {
		t.Errorf("%s still stands after the class was removed, and it would call a syncer nothing runs", schedule)
	}
	if _, err := cloudRun(t).Projects.Locations.Services.Get(servicePath).Context(ctx).Do(); err == nil {
		t.Errorf("%s still stands after the class was removed", servicePath)
	}
	for _, path := range accountPaths {
		if _, err := accounts(t).Projects.ServiceAccounts.Get(path).Context(ctx).Do(); err == nil {
			t.Errorf("%s still stands after the class was removed", path)
		}
	}
}
