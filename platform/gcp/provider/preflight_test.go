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
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

func declaringAStore(memory int64) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy:    provider.DeploySpec{Tier: environment.TierProduction},
		Resources: []provider.Resource{{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: memory}}},
	}
}

func TestAPublicBucketIsRefusedBeforeAnythingIsReadAndAPrivateOneNeedsNoPart(t *testing.T) {
	p := pushing(t, servingOnlyEnabledAPIs(t))
	declaring := func(spec provider.BucketSpec) provider.DeployPreflight {
		return provider.DeployPreflight{
			Deploy:    provider.DeploySpec{Tier: environment.TierProduction},
			Resources: []provider.Resource{{Name: "avatars", Type: provider.BindingBucket, Bucket: &spec}},
		}
	}

	err := p.ApplyPreflight(context.Background(), declaring(provider.BucketSpec{Public: true}))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(err.Error(), "avatars") {
		t.Errorf("ApplyPreflight() of a public bucket = %v, want an %s refusal naming it", err, refusal.CodeInvalid)
	}
	if err := p.ApplyPreflight(context.Background(), declaring(provider.BucketSpec{AllowedOrigins: []string{"https://shop.example"}})); err != nil {
		t.Errorf("ApplyPreflight() of a private bucket = %v, want it admitted", err)
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

	err := pushing(t, served.URL).ApplyPreflight(context.Background(), declaringADatabase("12"))
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("ApplyPreflight() = %v, want an %s refusal", err, refusal.CodeInvalid)
	}
}

func TestADeployDeclaringAStoreAbove32GBIsRefusedBeforeAnythingIsRead(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s before refusing a store above 32gb", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	err := pushing(t, served.URL).ApplyPreflight(context.Background(), declaringAStore(64<<30))
	if err == nil || !strings.Contains(err.Error(), "32gb") {
		t.Errorf("ApplyPreflight() = %v, want the store refused naming the 32gb limit", err)
	}
}

func TestADeployDeclaringNoStoreReadsNothing(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("preflight called %s %s for a deploy declaring no store", r.Method, r.URL.Path)
	}))
	t.Cleanup(served.Close)

	if err := pushing(t, served.URL).ApplyPreflight(context.Background(), provider.DeployPreflight{}); err != nil {
		t.Errorf("ApplyPreflight() = %v", err)
	}
}

func declaringATask() provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction, Infra: naming.InfraStack("prod")},
		Resources: []provider.Resource{{Name: "task-resize", Declared: "resize", Type: provider.BindingTask, Topic: &provider.TopicSpec{
			Consumers: []provider.ConsumerSpec{{Name: "resize", Worker: "worker", Exclusive: true}},
		}}},
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
		for _, front := range []edge.Kind{edge.None, cloudflare.Kind} {
			pre := shippingNext(container)
			pre.Edge = front
			p := pushing(t, servingOnlyEnabledAPIs(t))
			recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys, bootstrap.EdgeBucket, bootstrap.CacheBucket)
			if err := p.ApplyPreflight(context.Background(), pre); err != nil {
				t.Errorf("ApplyPreflight() of a Next app (container %v) behind %q = %v, want it admitted without reading the stamp: the edge refreshes a stale page", container, front, err)
			}
		}
	}
}

func TestADeployWithoutANextAppIsNotRefusedForAnEdgeThatCannotBeOpened(t *testing.T) {
	pre := shippingNext(false)
	pre.Deploy.Apps[0].Manifest.Framework = nil
	pre.Edge = edge.Kind("unregistered")
	if err := pushing(t, servingOnlyEnabledAPIs(t)).ApplyPreflight(context.Background(), pre); err != nil {
		t.Errorf("ApplyPreflight() = %v, want an app that is not Next admitted whatever edge fronts it", err)
	}
}

func TestTheFirstDeployDeclaringADatabaseAddsThePrivateNetworkAsAPartItRecords(t *testing.T) {
	p, server := deployingNetworks(t)
	ctx := context.Background()
	pre := declaringADatabase("17")
	pre.Deploy.Slug = "shop"
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)

	if err := p.ApplyPreflight(ctx, pre); err != nil {
		t.Fatalf("ApplyPreflight() = %v", err)
	}

	want := []string{"create network ocel-prod-network", "create subnetwork ocel-prod-network", "grant on ocel-prod-network", "create policy ocel-prod-network-memorystore", "create policy ocel-prod-network-cloudsql"}
	if got := server.wrote(); !slices.Equal(got, want) {
		t.Errorf("the first deploy declaring a database wrote %v, want %v", got, want)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, p.KeyValues(), environment.TierProduction, network.PartName)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" || len(recorded.Resources) != 4 {
		t.Errorf("the part record = %+v, %t, %v, want it ready, added by shop, holding the network, subnetwork and both policies", recorded, present, err)
	}

	if err := p.ApplyPreflight(ctx, declaringAStore(1<<30)); err != nil {
		t.Fatalf("ApplyPreflight() of a store = %v", err)
	}
	if got := server.wrote(); len(got) != len(want) {
		t.Errorf("a later deploy declaring a store wrote %v, want only its read of the part record", got[len(want):])
	}
}

func TestADryDeployDeclaringADatabasePlansThePrivateNetworkAndMakesNothing(t *testing.T) {
	p, server := deployingNetworks(t)
	plan, err := p.PlanPreflight(context.Background(), declaringADatabase("17"))
	if err != nil {
		t.Fatalf("PlanPreflight() = %v", err)
	}
	planned := plan.Groups

	if len(server.wrote()) != 0 {
		t.Errorf("a dry deploy wrote %v, want nothing", server.wrote())
	}
	if len(planned) != 2 || planned[0].Part != bootstrap.KeysPart || planned[1].Part != network.PartName || planned[1].Action != provider.ActionCreate || len(planned[1].Changes) != 4 {
		t.Errorf("a dry deploy planned %+v, want the keys its password is sealed under and the %s part created with its four resources", planned, network.PartName)
	}
}
