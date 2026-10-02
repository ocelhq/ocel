package providerserver

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func serverlessApp(name string) *contractv1.ManifestApp {
	return &contractv1.ManifestApp{
		Name:         name,
		DeploymentId: deploymentID,
		Artifact:     &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}},
	}
}

func TestEachAppIsHandedTheWorkersThatJoinIt(t *testing.T) {
	t.Parallel()

	req := productionRequest(serverlessApp("web"), serverlessApp("jobs"))
	req.Manifest.Workers = []*contractv1.ManifestWorker{
		{Name: "worker", App: "web", Path: ".", Compute: "serverless"},
		{Name: "media", Concurrency: 4, App: "jobs", Path: "apps/jobs", Compute: "serverless"},
		{Name: "reports", App: "jobs", Path: "apps/jobs", Compute: "serverless"},
	}
	spec, err := buildDeploySpec(req, "p1")
	if err != nil {
		t.Fatalf("buildDeploySpec() error = %v", err)
	}
	got := map[string][]provider.WorkerSpec{}
	for _, entry := range spec.Apps {
		got[entry.App] = entry.Workers
	}
	want := map[string][]provider.WorkerSpec{
		"web":  {{Name: "worker"}},
		"jobs": {{Name: "media", Concurrency: 4}, {Name: "reports"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("workers by app = %+v, want %+v", got, want)
	}
}

func TestAConsumerOnAWorkerTheManifestDoesNotDeclareIsRefusedAsInvalid(t *testing.T) {
	t.Parallel()

	manifest := &contractv1.Manifest{
		Slug:    "shop",
		Apps:    []*contractv1.ManifestApp{serverlessApp("web")},
		Workers: []*contractv1.ManifestWorker{{Name: "worker", App: "web", Compute: "serverless"}},
		Resources: []*contractv1.ManifestResource{{
			LogicalName: "topic--orders",
			Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, Name: "orders"},
			Config: &contractv1.ManifestResource_Topic{Topic: &contractv1.ManifestTopic{
				Consumers: []*contractv1.ManifestConsumer{{Name: "email", Worker: "worker"}, {Name: "thumbnails", Worker: "media"}},
			}},
		}},
	}
	_, err := manifestResources(manifest)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("manifestResources() = %v, want a refusal with code %s", err, refusal.CodeInvalid)
	}
	for _, said := range []string{`"thumbnails"`, `worker "media"`} {
		if !strings.Contains(err.Error(), said) {
			t.Errorf("manifestResources() = %q, want it to say %s", err, said)
		}
	}
}

func TestAWorkerNoAppCanRunIsRefusedAsInvalid(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		workers []*contractv1.ManifestWorker
		says    string
	}{
		{name: "no name", workers: []*contractv1.ManifestWorker{{App: "web", Compute: "serverless"}}, says: "worker name"},
		{name: "declared twice", workers: []*contractv1.ManifestWorker{{Name: "media", App: "web", Compute: "serverless"}, {Name: "media", App: "web", Compute: "serverless"}}, says: `worker "media" twice`},
		{name: "joining no app in the manifest", workers: []*contractv1.ManifestWorker{{Name: "media", App: "jobs", Compute: "serverless"}}, says: `app "jobs"`},
		{name: "on a compute its app does not run", workers: []*contractv1.ManifestWorker{{Name: "media", App: "web", Compute: "container"}}, says: "container"},
		{name: "a negative concurrency", workers: []*contractv1.ManifestWorker{{Name: "media", App: "web", Compute: "serverless", Concurrency: -2}}, says: "concurrency -2"},
		{name: "a concurrency above 1000", workers: []*contractv1.ManifestWorker{{Name: "media", App: "web", Compute: "serverless", Concurrency: 1001}}, says: "concurrency 1001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := productionRequest(serverlessApp("web"))
			req.Manifest.Workers = tc.workers
			_, err := buildDeploySpec(req, "p1")
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("buildDeploySpec() = %v, want a refusal with code %s", err, refusal.CodeInvalid)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("buildDeploySpec() = %q, want it to say %q", err, tc.says)
			}
		})
	}
}
