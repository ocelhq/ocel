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
)

func TestAWorkerIsAServiceOnlyPubSubReachesThatScalesToNothing(t *testing.T) {
	t.Parallel()

	worker := workerServing(provider.WorkerSpec{Name: "media", Concurrency: 4}, serving{
		service: "ocel-shop-prod-web-w-media-a1b2c3",
		image:   "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
		env:     map[string]string{"REGION": "eu"},
		account: "ocel-production@acme-prod.iam.gserviceaccount.com",
	})
	desired := desiredOf(t, worker)

	if desired.Ingress != ingressInternal {
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
	if template.ServiceAccount != "ocel-production@acme-prod.iam.gserviceaccount.com" {
		t.Errorf("a worker runs as %s, want the tier's workload account it sends and records runs as", template.ServiceAccount)
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

	names := Names{namespace: "ocel", project: "acme-prod"}
	app, err := names.Service("shop", stackrecords.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	media := names.WorkerService("shop", stackrecords.ProductionEnv, "web", "media")
	ledger := names.WorkerService("shop", stackrecords.ProductionEnv, "web", "ledger")
	if media == app || media == ledger || !strings.Contains(media, "media") || len(media) > maxServiceNameLength {
		t.Errorf("WorkerService() = %q and %q beside the app's %q, want a service of each worker's own in %d characters", media, ledger, app, maxServiceNameLength)
	}
	long := names.WorkerService("shop", stackrecords.ProductionEnv, "web", strings.Repeat("w", 40))
	longer := names.WorkerService("shop", stackrecords.ProductionEnv, "web", strings.Repeat("w", 41))
	if len(long) > maxServiceNameLength || long == longer || !cloudRunService.MatchString(long) {
		t.Errorf("WorkerService() of workers too long to spell out = %q and %q, want each cut to %d characters and kept apart by its hash", long, longer, maxServiceNameLength)
	}
}

func TestAnAppReachingTopicsIsPinnedEveryTopicItsWorkersAndProxyServe(t *testing.T) {
	t.Parallel()

	c := &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1"}
	declared := map[string]*provider.TopicSpec{"resize": {TTL: time.Hour}}
	tasks := tasksManifest(c, provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack(stackrecords.ProductionEnv)}, declared)

	if tasks.Environment != stackrecords.ProductionEnv || tasks.Topics["resize"].TTL != time.Hour {
		t.Errorf("tasksManifest() = %+v, want the environment's own runs and the resize topic as deployed", tasks)
	}
	if tasks.DelayQueue != "projects/acme-prod/locations/europe-west1/queues/ocel-production-delays" ||
		tasks.DelayAccount != "ocel-production@acme-prod.iam.gserviceaccount.com" || tasks.PublishURL != "https://pubsub.googleapis.com" {
		t.Errorf("tasksManifest() = %+v, want the tier's delay queue, its workload account and Pub/Sub's own url", tasks)
	}
}
