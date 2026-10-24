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
	"github.com/ocelhq/ocel/pkg/progress"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/originsecret"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

func declaringAStore(memory int64) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy:    provider.DeploySpec{Tier: environment.TierProduction},
		Resources: []provider.Resource{{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{MemoryBytes: memory}}},
	}
}

func TestAPublicBucketIsRefusedBeforeAnythingIsReadAndAPrivateOneIsAdmitted(t *testing.T) {
	p := pushing(t, servingOnlyEnabledAPIs(t))
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys)
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

var (
	containerApp = &contractv1.ManifestApp{Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{}}}
	functionApp  = &contractv1.ManifestApp{Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{
		Functions: []*contractv1.ManifestFunction{{LogicalName: "fn--web--index"}},
	}}}
	staticApp = &contractv1.ManifestApp{Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}}}
)

func applyingBehindTheWorker(t *testing.T, h *offersHarness, apps ...*contractv1.ManifestApp) []string {
	t.Helper()
	log := &fake.Log{}
	if len(apps) == 0 {
		apps = []*contractv1.ManifestApp{containerApp}
	}
	deploy := provider.DeploySpec{Tier: environment.TierProduction}
	for _, app := range apps {
		deploy.Apps = append(deploy.Apps, provider.AppEntry{App: "web", Manifest: app})
	}
	pre := provider.DeployPreflight{Deploy: deploy, Edge: cloudflare.Kind, Progress: log}
	if err := programming(h).warnStaleOriginSecret(t.Context(), pre); err != nil {
		t.Fatalf("warnStaleOriginSecret() = %v", err)
	}
	return log.Lines()
}

func TestADeployBehindTheWorkerIsWarnedThatItsTiersOriginSecretIsStale(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	h.recordOriginSecretAs(t, originsecret.Secret{Current: "s1", CreatedAt: time.Now().Add(-originsecret.MaxAge - 24*time.Hour)})

	lines := strings.Join(applyingBehindTheWorker(t, h), "\n")
	if !strings.Contains(lines, "WARN The production origin secret") || !strings.Contains(lines, "ocel bootstrap") {
		t.Errorf("the deploy said %q, want a warning that the origin secret is stale and bootstrap rotates it", lines)
	}
}

func TestADeployBehindTheWorkerOnAFreshOriginSecretIsWarnedOfNothing(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	h.recordOriginSecretAs(t, originsecret.Secret{Current: "s1", CreatedAt: time.Now().Add(-24 * time.Hour)})

	if lines := applyingBehindTheWorker(t, h); slices.ContainsFunc(lines, func(line string) bool { return strings.Contains(line, "origin secret") }) {
		t.Errorf("the deploy said %q, want nothing of a secret a day old", lines)
	}
}

func TestADeployBehindTheWorkerWarnsOfAStaleOriginSecretOnlyWhenAnAppRunsCode(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		apps   []*contractv1.ManifestApp
		warned bool
	}{
		"a container":          {apps: []*contractv1.ManifestApp{containerApp}, warned: true},
		"a function":           {apps: []*contractv1.ManifestApp{functionApp}, warned: true},
		"a static app and one": {apps: []*contractv1.ManifestApp{staticApp, functionApp}, warned: true},
		"static files alone":   {apps: []*contractv1.ManifestApp{staticApp}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newOffersHarness(t)
			h.recordOriginSecretAs(t, originsecret.Secret{Current: "s1", CreatedAt: time.Now().Add(-originsecret.MaxAge - 24*time.Hour)})

			lines := applyingBehindTheWorker(t, h, tc.apps...)
			if warned := slices.ContainsFunc(lines, func(line string) bool { return strings.Contains(line, "origin secret") }); warned != tc.warned {
				t.Errorf("the deploy said %q, want a stale origin secret warned of %v: only a release that runs code demands the secret", lines, tc.warned)
			}
		})
	}
}

func plannedBy(t *testing.T, p *Provider, pre provider.DeployPreflight) []provider.ChangeGroup {
	t.Helper()
	plan, err := p.PlanPreflight(context.Background(), pre)
	if err != nil {
		t.Fatalf("PlanPreflight() = %v", err)
	}
	return plan.Groups
}

func deployingProject(project string) provider.DeployPreflight {
	return provider.DeployPreflight{
		Deploy: provider.DeploySpec{Tier: environment.TierProduction, Slug: project},
		Uses:   []buildoutput.Uses{{Revalidation: true}},
	}
}

func cachingProvider(t *testing.T, p *Provider, own bool) *Provider {
	t.Helper()
	p.options.OwnDatabase = own
	recordedCurrent(t, p, environment.TierProduction, bootstrap.CacheBucket)
	return p
}

func plansSharedDatabase(planned []provider.ChangeGroup) bool {
	return len(planned) == 1 && planned[0].Part == variables.SharedDatabasePartName && planned[0].Action == provider.ActionCreate
}

func plansOwnDatabase(planned []provider.ChangeGroup, database string) bool {
	return len(planned) == 1 && planned[0].Action == provider.ActionCreate && planned[0].Reason == bootstrap.ReasonFirstUse && len(planned[0].Changes) == 1 &&
		planned[0].Changes[0].Name == database && planned[0].Changes[0].Action == provider.ActionCreate
}

func TestTheFirstDryDeployOfATierPlansTheDatabaseItsProjectsShareAndMakesNothing(t *testing.T) {
	p := cachingProvider(t, failingOnAnyCall(t), false)
	if planned := plannedBy(t, p, deployingProject("shop")); !plansSharedDatabase(planned) {
		t.Errorf("a first dry deploy of shop planned %+v, want the %s part added", planned, variables.SharedDatabasePartName)
	}
}

func TestTheFirstDryDeployOfAProjectWithOwnDatabasePlansItsOwnDatabaseAndMakesNothing(t *testing.T) {
	p := cachingProvider(t, failingOnAnyCall(t), true)
	if planned := plannedBy(t, p, deployingProject("shop")); !plansOwnDatabase(planned, ports.OwnDatabase("ocel", environment.TierProduction, "shop")) {
		t.Errorf("a first dry deploy of shop with ownDatabase planned %+v, want its own database created", planned)
	}
}

func TestAProjectsFirstDeployRecordsItsDatabaseAndTheNextPlansNone(t *testing.T) {
	for _, own := range []bool{false, true} {
		p, _ := deployingNetworks(t)
		cachingProvider(t, p, own)
		ctx := context.Background()

		if err := p.ApplyPreflight(ctx, deployingProject("shop")); err != nil {
			t.Fatalf("ApplyPreflight(ownDatabase %t) = %v", own, err)
		}
		if planned := plannedBy(t, p, deployingProject("shop")); len(planned) != 0 {
			t.Errorf("with ownDatabase %t, a dry deploy after the first planned %+v, want nothing: the first recorded the database it keeps its records in", own, planned)
		}
		if planned := plannedBy(t, p, deployingProject("blog")); own && !plansOwnDatabase(planned, ports.OwnDatabase("ocel", environment.TierProduction, "blog")) {
			t.Errorf("a dry deploy of blog with ownDatabase planned %+v, want blog's own database created", planned)
		}
	}
}

func TestADeployThatTurnsOnOwnDatabaseAfterTheProjectsFirstIsRefusedBeforeAnyChange(t *testing.T) {
	p, _ := deployingNetworks(t)
	cachingProvider(t, p, false)
	ctx := context.Background()
	if err := p.ApplyPreflight(ctx, deployingProject("shop")); err != nil {
		t.Fatal(err)
	}
	p.options.OwnDatabase = true

	if _, err := p.PlanPreflight(ctx, deployingProject("shop")); err == nil || !strings.Contains(err.Error(), "ownDatabase") {
		t.Errorf("planning shop with ownDatabase after it shared its tier's database = %v, want a refusal naming ownDatabase", err)
	}
}

func TestRemovingAProjectWithItsOwnDatabaseForgetsItSoItsNextDeployMakesItAgain(t *testing.T) {
	p, _ := deployingNetworks(t)
	cachingProvider(t, p, true)
	ctx := context.Background()
	if err := p.ApplyPreflight(ctx, deployingProject("shop")); err != nil {
		t.Fatalf("ApplyPreflight() = %v", err)
	}

	if err := p.RemoveProject(ctx, environment.TierProduction, "shop", progress.Discard()); err != nil {
		t.Fatalf("RemoveProject() = %v", err)
	}
	if planned := plannedBy(t, p, deployingProject("shop")); !plansOwnDatabase(planned, ports.OwnDatabase("ocel", environment.TierProduction, "shop")) {
		t.Errorf("a dry deploy after shop left the tier planned %+v, want its database made again", planned)
	}
}
