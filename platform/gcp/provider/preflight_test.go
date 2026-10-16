package gcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func withStamp(t *testing.T, written stamp) *Provider {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/ocel-acme-prod-production/"+StampObject) {
			t.Errorf("preflight called %s %s, want only the tier's bootstrap stamp read", r.Method, r.URL.Path)
		}
		writeBody(w, written)
	}))
	t.Cleanup(served.Close)
	return pushing(t, served.URL)
}

func declaringAStore(memory int64) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy:    provider.DeploySpec{Tier: environment.TierProduction},
		Resources: []provider.Resource{{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: memory}}},
	}
}

func TestAPublicBucketIsRefusedBeforeAnythingIsReadAndAPrivateOneNeedsNoFeature(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s, and a bucket needs nothing of the tier's bootstrap to be judged", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)
	p := pushing(t, served.URL)
	declaring := func(spec provider.BucketSpec) provider.DeployPreflight {
		return provider.DeployPreflight{
			Deploy:    provider.DeploySpec{Tier: environment.TierProduction},
			Resources: []provider.Resource{{Name: "avatars", Type: provider.BindingBucket, Bucket: &spec}},
		}
	}

	err := p.PreflightDeploy(context.Background(), declaring(provider.BucketSpec{Public: true}))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "avatars") {
		t.Errorf("PreflightDeploy() of a public bucket = %v, want an %s refusal naming it", err, refusal.CodeInvalid)
	}
	if err := p.PreflightDeploy(context.Background(), declaring(provider.BucketSpec{AllowedOrigins: []string{"https://shop.example"}})); err != nil {
		t.Errorf("PreflightDeploy() of a private bucket = %v, want it admitted", err)
	}
}

func declaringADatabase(version string) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy:    provider.DeploySpec{Tier: environment.TierProduction},
		Resources: []provider.Resource{{Name: "orders", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{Version: version}}},
	}
}

func TestADeployDeclaringADatabaseOnAVersionCloudSQLDoesNotRunIsRefusedBeforeAnythingIsRead(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s before refusing a version Cloud SQL does not run", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	err := pushing(t, served.URL).PreflightDeploy(context.Background(), declaringADatabase("12"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("PreflightDeploy() = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
}

func TestADeployDeclaringAStoreAbove32GBIsRefusedBeforeAnythingIsRead(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s before refusing a store above 32gb", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	err := pushing(t, served.URL).PreflightDeploy(context.Background(), declaringAStore(64<<30))
	if err == nil || !strings.Contains(err.Error(), "32gb") {
		t.Errorf("PreflightDeploy() = %v, want the store refused naming the 32gb limit", err)
	}
}

func TestADeployDeclaringNoStoreReadsNothing(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s for a deploy declaring no store", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	if err := pushing(t, served.URL).PreflightDeploy(context.Background(), provider.DeployPreflight{}); err != nil {
		t.Errorf("PreflightDeploy() = %v", err)
	}
}

func declaringATask(infra naming.StackName) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction, Infra: infra},
		Resources: []provider.Resource{{Name: "task-resize", Declared: "resize", Type: provider.BindingTask, Topic: &provider.TopicSpec{
			Consumers: []provider.ConsumerSpec{{Name: "resize", Worker: "worker", Exclusive: true}},
		}}},
	}
}

func TestADeployDeclaringATaskIsAdmittedOnceTheTierHasTasks(t *testing.T) {
	p := withStamp(t, stamp{State: stateComplete, Features: []string{tasksFeature}})
	if err := p.PreflightDeploy(context.Background(), declaringATask(naming.InfraStack("prod"))); err != nil {
		t.Errorf("PreflightDeploy() = %v, want a task admitted on a tier whose tasks feature is installed", err)
	}
}

func TestADeployDeclaringATaskIsRefusedUntilTheTierHasTasks(t *testing.T) {
	for _, written := range []stamp{{State: stateComplete}, {State: stateApplying, Features: []string{tasksFeature}}} {
		err := withStamp(t, written).PreflightDeploy(context.Background(), declaringATask(naming.InfraStack("prod")))
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady ||
			!strings.Contains(err.Error(), "ocel bootstrap production --features "+tasksFeature) {
			t.Errorf("PreflightDeploy() under stamp %+v = %v, want it refused naming the bootstrap that installs topics and tasks", written, err)
		}
	}
}

func TestAnEphemeralPreviewDeclaringATaskIsRefusedBeforeAnythingIsRead(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s before refusing an ephemeral preview's task", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	err := pushing(t, served.URL).PreflightDeploy(context.Background(), declaringATask(naming.StackName{}))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported || !strings.Contains(err.Error(), "resize") {
		t.Errorf("PreflightDeploy() of an ephemeral preview declaring a task = %v, want an %s refusal naming it: such a preview provisions no infra stack", err, refusal.CodeUnsupported)
	}
}

func TestWorkersRunOnCloudRunForAtMostThe600sAPushMayTake(t *testing.T) {
	want := []provider.WorkerCeiling{
		{Compute: provider.ComputeServerless, MaxDuration: 600 * time.Second},
		{Compute: provider.ComputeContainer, MaxDuration: 600 * time.Second},
	}
	if got := pushing(t, "").Facts().WorkerCeilings; !slices.Equal(got, want) {
		t.Errorf("Facts().WorkerCeilings = %+v, want %+v: a push subscription waits at most 600s for its endpoint", got, want)
	}
	if bindings := pushing(t, "").Facts().Bindings; !slices.Contains(bindings, provider.BindingTopic) || !slices.Contains(bindings, provider.BindingTask) {
		t.Errorf("Facts().Bindings = %v, want topics and tasks served", bindings)
	}
}

func shippingNext(container bool) provider.DeployPreflight {
	app := &contractv1.ManifestApp{Name: "web", Framework: &contractv1.Framework{Name: buildoutput.FrameworkNext}}
	if container {
		app.Artifact = &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{}}
	} else {
		app.Artifact = &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}}
	}
	return provider.DeployPreflight{Deploy: provider.DeploySpec{Tier: environment.TierProduction, Apps: []provider.AppEntry{{App: "web", Manifest: app}}}}
}

func failingOnAnyCall(t *testing.T) *Provider {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s, want no call", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)
	return pushing(t, served.URL)
}

func TestANextAppIsAdmittedWithoutReadingWhatTheTierHasInstalled(t *testing.T) {
	for _, container := range []bool{false, true} {
		for _, front := range []edge.Kind{edge.None, alb.Kind, cloudflare.Kind} {
			pre := shippingNext(container)
			pre.Edge = front
			if err := failingOnAnyCall(t).PreflightDeploy(context.Background(), pre); err != nil {
				t.Errorf("PreflightDeploy() of a Next app (container %v) behind %q = %v, want it admitted without reading the stamp: the edge refreshes a stale page", container, front, err)
			}
		}
	}
}

func TestADeployWithoutANextAppIsNotRefusedForAnEdgeThatCannotBeOpened(t *testing.T) {
	pre := shippingNext(false)
	pre.Deploy.Apps[0].Manifest.Framework = nil
	pre.Edge = edge.Kind("unregistered")
	if err := failingOnAnyCall(t).PreflightDeploy(context.Background(), pre); err != nil {
		t.Errorf("PreflightDeploy() = %v, want an app that is not Next admitted whatever edge fronts it", err)
	}
}

func TestTheFirstDeployDeclaringADatabaseAddsThePrivateNetworkAsAPartItRecords(t *testing.T) {
	p, server := deployingNetworks(t)
	ctx := context.Background()
	pre := declaringADatabase("17")
	pre.Deploy.Slug = "shop"

	if err := p.PreflightDeploy(ctx, pre); err != nil {
		t.Fatalf("PreflightDeploy() = %v", err)
	}

	want := []string{"create network ocel-production", "create subnetwork ocel-production", "create policy ocel-production-memorystore", "create policy ocel-production-cloudsql"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("the first deploy declaring a database wrote %v, want %v", got, want)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), environment.TierProduction, networkPart)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" || len(recorded.Resources) != 4 {
		t.Errorf("the part record = %+v, %t, %v, want it ready, added by shop, holding the network, subnetwork and both policies", recorded, present, err)
	}

	if err := p.PreflightDeploy(ctx, declaringAStore(1<<30)); err != nil {
		t.Fatalf("PreflightDeploy() of a store = %v", err)
	}
	if got := server.wrote(); len(got) != len(want) {
		t.Errorf("a later deploy declaring a store wrote %v, want only its read of the part record", got[len(want):])
	}
}

func TestADryDeployDeclaringADatabasePlansThePrivateNetworkAndMakesNothing(t *testing.T) {
	p, server := deployingNetworks(t)
	var planned []provider.ChangeGroup
	pre := declaringADatabase("17")
	pre.Dry = true
	pre.AddToPlan = func(groups ...provider.ChangeGroup) { planned = append(planned, groups...) }

	if err := p.PreflightDeploy(context.Background(), pre); err != nil {
		t.Fatalf("PreflightDeploy() = %v", err)
	}

	if len(server.wrote()) != 0 {
		t.Errorf("a dry deploy wrote %v, want nothing", server.wrote())
	}
	if len(planned) != 1 || planned[0].Feature != networkPart || planned[0].Action != provider.ActionCreate || len(planned[0].Changes) != 4 {
		t.Errorf("a dry deploy planned %+v, want the %s part created with its four resources", planned, networkPart)
	}
}
