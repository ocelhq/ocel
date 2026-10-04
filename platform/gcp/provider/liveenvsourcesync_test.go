//go:build integration

package gcp_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"google.golang.org/api/cloudscheduler/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
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

func posted(t *testing.T, uri string) int {
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

func TestLiveATierRunsItsEnvSourceSyncAndRemovingTheTierTakesItAway(t *testing.T) {
	p := live(t)
	servicesEnabled(t)
	tier := environment.TierPreview
	names := liveNames(t)
	ctx := context.Background()

	bootstrap := bootstrapOf(t, p)
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", tier, err)
	}
	location := "projects/" + liveProject() + "/locations/" + liveRegion()
	servicePath := location + "/services/" + names.EnvSourceSync(tier)
	service, err := cloudRun(t).Projects.Locations.Services.Get(servicePath).Context(ctx).Do()
	if err != nil {
		t.Fatalf("Get(%s) after a bootstrap = %v, want the service the sync answers on", servicePath, err)
	}
	if service.Ingress != "INGRESS_TRAFFIC_INTERNAL_ONLY" || service.InvokerIamDisabled {
		t.Errorf("the sync takes %s with invokerIamDisabled %t, want internal traffic from a caller with run.invoker", service.Ingress, service.InvokerIamDisabled)
	}
	container := service.Template.Containers[0]
	if !container.Resources.CpuIdle || !slices.Contains([]string{"0.08", "80m"}, container.Resources.Limits["cpu"]) || service.Template.Scaling.MaxInstanceCount != 1 {
		t.Errorf("the sync runs %+v scaled %+v, want request-based billing on 0.08 vCPU and one instance at most", container.Resources, service.Template.Scaling)
	}
	policy, err := cloudRun(t).Projects.Locations.Services.GetIamPolicy(servicePath).Context(ctx).Do()
	if err != nil {
		t.Fatalf("GetIamPolicy(%s) = %v", servicePath, err)
	}
	member := "serviceAccount:" + names.EnvSourceSyncAccountEmail(tier)
	if !slices.ContainsFunc(policy.Bindings, func(binding *run.GoogleIamV1Binding) bool {
		return binding.Role == "roles/run.invoker" && slices.Equal(binding.Members, []string{member})
	}) {
		t.Errorf("the sync's policy binds %+v, want %s alone with roles/run.invoker", policy.Bindings, member)
	}

	schedule := location + "/jobs/" + names.EnvSourceSync(tier)
	created, err := schedules(t).Projects.Locations.Jobs.Get(schedule).Context(ctx).Do()
	if err != nil {
		t.Fatalf("Get(%s) after a bootstrap = %v, want the schedule that calls the sync", schedule, err)
	}
	if created.Schedule != "* * * * *" || created.HttpTarget == nil || created.HttpTarget.Uri != service.Uri+"/" || created.HttpTarget.HttpMethod != http.MethodPost {
		t.Errorf("the sync is called %+v on %q, want POST %s/ every minute", created.HttpTarget, created.Schedule, service.Uri)
	}
	if emulated() {
		if status := posted(t, reachable(t, service.Uri)+"/"); status != http.StatusNoContent {
			t.Errorf("POST / on the sync = %d, want 204: a sync answers 204 even when it fails, so Cloud Scheduler adds no retry to the sync's own backoff", status)
		}
	}

	account := "projects/" + liveProject() + "/serviceAccounts/" + names.EnvSourceSyncAccountEmail(tier)
	if _, err := accounts(t).Projects.ServiceAccounts.Get(account).Context(ctx).Do(); err != nil {
		t.Errorf("Get(%s) after a bootstrap = %v, want the account", account, err)
	}
	described, err := bootstrap.Describe(ctx, tier)
	if err != nil || len(described.Stacks) != 1 || !described.Stacks[0].DigestCurrent {
		t.Errorf("Describe() = %+v, %v, want the stack current with the sync in place", described, err)
	}
	plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() after a bootstrap = %v", err)
	}
	rows := rowsOf(t, plan)
	for _, id := range []string{"run:service/" + names.EnvSourceSync(tier), "cloudscheduler:job/" + names.EnvSourceSync(tier), "iam:serviceaccount/" + names.EnvSourceSyncAccount(tier)} {
		if row := rows[id]; row.Action != provider.ActionKeep {
			t.Errorf("Plan() after a bootstrap shows %s as %q (%q), want it kept: what the bootstrap created reads as current", id, row.Action, row.Reason)
		}
	}

	if _, err := schedules(t).Projects.Locations.Jobs.Pause(schedule, &cloudscheduler.PauseJobRequest{}).Context(ctx).Do(); err != nil {
		t.Fatalf("Pause(%s) = %v", schedule, err)
	}
	plan, err = bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() over a paused schedule = %v", err)
	}
	if row := rowsOf(t, plan)["cloudscheduler:job/"+names.EnvSourceSync(tier)]; row.Action != provider.ActionUpdate {
		t.Errorf("Plan() shows a paused schedule as %q (%q), want it mended: it never calls the sync", row.Action, row.Reason)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) over a paused schedule = %v", tier, err)
	}
	if resumed, err := schedules(t).Projects.Locations.Jobs.Get(schedule).Context(ctx).Do(); err != nil || resumed.State != "ENABLED" {
		t.Errorf("Get(%s) after the mend = %+v, %v, want it enabled", schedule, resumed, err)
	}

	if err := bootstrap.Remove(ctx, tier, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", tier, err)
	}
	if _, err := schedules(t).Projects.Locations.Jobs.Get(schedule).Context(ctx).Do(); err == nil {
		t.Errorf("%s still exists after the tier was removed, and it would call a sync nothing runs", schedule)
	}
	if _, err := cloudRun(t).Projects.Locations.Services.Get(servicePath).Context(ctx).Do(); err == nil {
		t.Errorf("%s still exists after the tier was removed", servicePath)
	}
	if _, err := accounts(t).Projects.ServiceAccounts.Get(account).Context(ctx).Do(); err == nil {
		t.Errorf("%s still exists after the tier was removed", account)
	}
}
