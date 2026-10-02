package live

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
)

func complete() Manifest {
	return Manifest{
		Project: "acme-prod", Region: "europe-west1", Namespace: "ocel", Slug: "shop", Tier: "production",
		Keys: []live.Key{{Key: "DATABASE_URL"}},
	}
}

func TestAManifestPinningTopicsAndTasksRendersEvenWithNoValueLive(t *testing.T) {
	t.Parallel()
	manifest := complete()
	manifest.Keys = nil
	manifest.Tasks = &Tasks{
		Environment: "production",
		Topics: map[string]provider.TopicSpec{"resize": {TTL: time.Hour, Consumers: []provider.ConsumerSpec{
			{Name: "resize", Worker: "media", Exclusive: true, Retry: provider.RetryPolicy{MaxAttempts: 3, MinDelay: time.Second, MaxDelay: time.Minute}},
		}}},
		DelayQueue:   "projects/acme-prod/locations/europe-west1/queues/ocel-production-delays",
		DelayAccount: "ocel-production@acme-prod.iam.gserviceaccount.com",
		PublishURL:   "https://pubsub.googleapis.com",
	}
	rendered, err := Render(manifest)
	if err != nil || rendered == nil {
		t.Fatalf("Render() = %q, %v, want the topics a worker and the binding proxy serve pinned", rendered, err)
	}
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Tasks == nil || !reflect.DeepEqual(*parsed.Tasks, *manifest.Tasks) {
		t.Errorf("Parse(Render()).Tasks = %+v, want %+v", parsed.Tasks, manifest.Tasks)
	}
}

func TestAManifestNamingNothingLiveRendersToNothing(t *testing.T) {
	t.Parallel()
	manifest := complete()
	manifest.Keys = nil
	rendered, err := Render(manifest)
	if err != nil || rendered != nil {
		t.Errorf("Render() = %q, %v, want nothing: a container with no live value boots with no manifest and opens no store", rendered, err)
	}
}

func TestARenderedManifestParsesBackToWhatWasPinned(t *testing.T) {
	t.Parallel()
	manifest := complete()
	manifest.Tier, manifest.Environment = "preview", "pr-7"
	rendered, err := Render(manifest)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	parsed, err := Parse(rendered)
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if parsed.Project != manifest.Project || parsed.Region != manifest.Region || parsed.Namespace != manifest.Namespace ||
		parsed.Slug != manifest.Slug || parsed.Tier != manifest.Tier || parsed.Environment != manifest.Environment ||
		len(parsed.Keys) != 1 || parsed.Keys[0].Key != "DATABASE_URL" {
		t.Errorf("Parse(Render()) = %+v, want %+v", parsed, manifest)
	}
	if strings.Contains(string(rendered), "endpoint") {
		t.Errorf("a manifest against the real project contains %s, and an endpoint there would send the runtime past Google", rendered)
	}
}

func TestAManifestMissingWhatAddressesTheStoreIsRefused(t *testing.T) {
	t.Parallel()
	for name, sabotage := range map[string]func(*Manifest){
		"project":                  func(m *Manifest) { m.Project = "" },
		"region":                   func(m *Manifest) { m.Region = "" },
		"namespace":                func(m *Manifest) { m.Namespace = "" },
		"slug":                     func(m *Manifest) { m.Slug = "" },
		"tier":                     func(m *Manifest) { m.Tier = "" },
		"a tier nothing is called": func(m *Manifest) { m.Tier = "staging" },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := complete()
			sabotage(&manifest)
			if _, err := Render(manifest); err == nil {
				t.Errorf("Render() without %s = nil, want a refusal: the runtime would boot with nowhere to read from", name)
			}
		})
	}
}
