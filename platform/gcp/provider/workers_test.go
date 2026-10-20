package gcp

import (
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

func TestAWorkerIsAServiceOnlyPubSubReachesThatScalesToNothing(t *testing.T) {
	t.Parallel()

	worker := release.WorkerServing(provider.WorkerSpec{Name: "media", Concurrency: 4}, run.Serving{
		Service: "ocel-shop-prod-web-w-media-a1b2c3",
		Image:   "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
		Env:     map[string]string{"REGION": "eu"},
		Account: "ocel-1a2b3c4d5e@acme-prod.iam.gserviceaccount.com",
	})
	desired := desiredOf(t, worker)

	if desired.Ingress != run.IngressInternal {
		t.Errorf("a worker takes %s traffic, want internal alone: Pub/Sub pushes to it from inside the project", desired.Ingress)
	}
	if desired.InvokerIamDisabled {
		t.Error("a worker answers anyone, and only the push account may invoke one")
	}
	template := desired.Template
	if template.Scaling.MinInstanceCount != 0 || !template.Containers[0].Resources.CpuIdle {
		t.Errorf("a worker keeps %d instances up with idle cpu %v, want none and billing per push", template.Scaling.MinInstanceCount, template.Containers[0].Resources.CpuIdle)
	}
	if template.Timeout != "600s" {
		t.Errorf("a worker's request may run %s, want the 600s a push subscription waits", template.Timeout)
	}
	if template.MaxInstanceRequestConcurrency != 4 {
		t.Errorf("a worker instance takes %d pushes at once, want the 4 the worker declares", template.MaxInstanceRequestConcurrency)
	}
	if template.ServiceAccount != "ocel-1a2b3c4d5e@acme-prod.iam.gserviceaccount.com" {
		t.Errorf("a worker runs as %s, want the app's own account it was handed", template.ServiceAccount)
	}
	env := map[string]string{}
	for _, entry := range template.Containers[0].Env {
		env[entry.Name] = entry.Value
	}
	if env[processenv.WorkerEnvVar] != "media" || env["REGION"] != "eu" {
		t.Errorf("a worker's revision sets %v, want %s=media beside the app's own values", env, processenv.WorkerEnvVar)
	}
	if template.Containers[0].StartupProbe != nil {
		t.Error("a worker is probed on a path, and its front answers pushes, not the app's health path")
	}
}

func TestAWorkerIsNamedApartFromItsAppAndEveryOtherWorker(t *testing.T) {
	t.Parallel()

	named := names.New("ocel", "acme-prod")
	app, err := named.Service("shop", stackrecords.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	media, err := named.WorkerService("shop", stackrecords.ProductionEnv, "web", "media")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := named.WorkerService("shop", stackrecords.ProductionEnv, "web", "ledger")
	if err != nil {
		t.Fatal(err)
	}
	if media == app || media == ledger || !strings.Contains(media, "media") || len(media) > names.MaxServiceNameLength {
		t.Errorf("WorkerService() = %q and %q beside the app's %q, want a service of each worker's own in %d characters", media, ledger, app, names.MaxServiceNameLength)
	}
}

func TestAWorkerTooLongForACloudRunServiceIsRefusedRatherThanCut(t *testing.T) {
	t.Parallel()

	if long, err := names.New("ocel", "acme-prod").WorkerService("shop", stackrecords.ProductionEnv, "web", strings.Repeat("w", 40)); err == nil {
		t.Errorf("WorkerService() = %q, want a refusal: a worker's name is never cut, and Cloud Run takes %d characters", long, names.MaxServiceNameLength)
	}
}

func TestATasksManifestPinsTheTiersQueueAndPublishesDelaysAsTheAppsOwnAccount(t *testing.T) {
	t.Parallel()

	c := &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}
	declared := map[string]*provider.TopicSpec{"resize": {TTL: time.Hour}}
	tasks := topic.Manifest(c, provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack(stackrecords.ProductionEnv)}, "web", declared)

	if tasks.Environment != stackrecords.ProductionEnv || tasks.Topics["resize"].TTL != time.Hour {
		t.Errorf("tasksManifest() = %+v, want the environment's own runs and the resize topic as deployed", tasks)
	}
	if tasks.DelayQueue != "projects/acme-prod/locations/europe-west1/queues/ocel-prod-delays" ||
		tasks.Account != c.AppAccountEmail(environment.TierProduction, "shop", stackrecords.ProductionEnv, "web") || tasks.PublishURL != "https://pubsub.googleapis.com" {
		t.Errorf("tasksManifest() = %+v, want the tier's delay queue, the app's own account and Pub/Sub's own url", tasks)
	}
}
