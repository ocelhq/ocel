package gcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	variables "github.com/ocelhq/ocel/platform/gcp/provider/live"
)

func previewSpec() provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Tier:    environment.TierPreview,
			Name:    naming.StackName{Env: "pr-7", App: "web"},
		},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:             "web",
			Compute:         provider.ComputeContainer,
			Image:           "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
			HealthCheckPath: "/",
			Functions: []provider.FunctionSpec{{
				Name:      "fn--web--checkout",
				Image:     "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:abc",
				Framework: buildoutput.Framework{Name: "nodejs", Arch: string(arch.X8664)},
			}},
		},
	}
}

func TestAPreviewOnAnEdgeThatShieldsNothingIsSaidToBeOpenToAnyoneWithItsUrl(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	progress := &fake.Log{}

	if _, err := p.ProvisionContainers(context.Background(), previewSpec(), progress); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if !slices.ContainsFunc(progress.Lines(), func(said string) bool {
		return strings.HasPrefix(said, "WARN ") && strings.Contains(said, "is a preview and answers anyone")
	}) {
		t.Errorf("the release said %q, want it to warn the preview answers anyone with its url: Cloud Run has no invoker a browser could satisfy, so the reader has to know", progress.Lines())
	}

	production := &fake.Log{}
	spec := previewSpec()
	spec.Ref.Tier = environment.TierProduction
	spec.Ref.Name = naming.StackName{Env: stackrecords.ProductionEnv, App: "web"}
	if _, err := p.ProvisionContainers(context.Background(), spec, production); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if slices.ContainsFunc(production.Lines(), func(said string) bool { return strings.Contains(said, "is a preview") }) {
		t.Errorf("a production release said %q, and production is meant to answer anyone", production.Lines())
	}

	shielded := &fake.Log{}
	front, err := p.Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec = previewSpec()
	spec.Edge = front
	if _, err := p.ProvisionContainers(context.Background(), spec, shielded); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if slices.ContainsFunc(shielded.Lines(), func(said string) bool { return strings.Contains(said, "is a preview") }) {
		t.Errorf("a preview behind the load balancer said %q, and its service takes traffic from the load balancer alone", shielded.Lines())
	}
}

func TestAProductionReleaseIsNamedForItsNamespaceProjectEnvironmentAndApp(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := previewSpec()
	spec.Ref.Tier = environment.TierProduction
	spec.Ref.Name = naming.StackName{Env: stackrecords.ProductionEnv, App: "web"}

	containers, err := p.ProvisionContainers(context.Background(), spec, nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	want, err := names(t, p).Service("shop", stackrecords.ProductionEnv, "web", "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 || containers[0].Physical != want {
		t.Errorf("ProvisionContainers() deployed %+v, want %q: production is named for the namespace, project, environment and app as it always was",
			containers, want)
	}
}

func TestAFunctionGetsWhatTheDeployDeliveredAndWhatItsSpecNames(t *testing.T) {
	values, err := mergedValues("fn", map[string]string{"DATABASE_URL": "postgres://"}, map[string]string{"STAGE": "one"})
	if err != nil {
		t.Fatalf("mergedValues() = %v", err)
	}
	if values["DATABASE_URL"] != "postgres://" || values["STAGE"] != "one" {
		t.Errorf("mergedValues() = %v, want both what the deploy resolved and what the spec named", values)
	}
}

func TestASpecThatNamesWhatTheDeployDeliveredIsRefused(t *testing.T) {
	_, err := mergedValues("fn", map[string]string{"DATABASE_URL": "postgres://"}, map[string]string{"DATABASE_URL": "sqlite://"})
	if err == nil {
		t.Fatal("mergedValues() let the spec take the delivered value's place, and the app would read a value nothing in it declared")
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("mergedValues() code = %v, want %v", code, refusal.CodeInvalid)
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("mergedValues() = %v, want the name that would be displaced said", err)
	}
	if !strings.Contains(err.Error(), "fn") {
		t.Errorf("mergedValues() = %v, want what sets it said", err)
	}
}

func functionRelease(buildID, image string) provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Tier:    environment.TierProduction,
			Name:    naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken(buildID, "f1")),
		},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:     "web",
			Compute: provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{
				Name:      "fn--web--checkout",
				Image:     image,
				Framework: buildoutput.Framework{Name: "nodejs", Arch: string(arch.X8664)},
			}},
		},
	}
}

type releasedStacks struct {
	stacks provider.Stacks
	store  keyvalue.Store
}

func newReleasedStacks(p *Provider) releasedStacks {
	store := fake.NewKeyValues()
	p.records = store
	return releasedStacks{stacks: resources.NewHookStacks(store, resources.NoArtifacts{}, p.resourceHooks()), store: store}
}

func (r releasedStacks) provision(t *testing.T, spec provider.StackSpec) provider.Function {
	t.Helper()
	ctx := context.Background()
	result, err := r.stacks.Provision(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Provision(%s) = %v", spec.Ref.Name, err)
	}
	if err := stackrecords.Write(ctx, r.store, spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name,
		stackrecords.Stack{Kind: provider.StackApp, Functions: result.Functions}); err != nil {
		t.Fatal(err)
	}
	if len(result.Functions) != 1 {
		t.Fatalf("Provision(%s) deployed %+v, want the one function its spec names", spec.Ref.Name, result.Functions)
	}
	return result.Functions[0]
}

func (r releasedStacks) destroy(t *testing.T, ref provider.StackRef) {
	t.Helper()
	ctx := context.Background()
	if err := r.stacks.Destroy(ctx, ref, nil, nil); err != nil {
		t.Fatalf("Destroy(%s) = %v", ref.Name, err)
	}
	if err := stackrecords.Forget(ctx, r.store, ref.Tier, ref.Project, ref.Name); err != nil {
		t.Fatal(err)
	}
}

func TestReclaimingADroppedFunctionReleaseLeavesTheServiceServingTheActiveRelease(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	dropped := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one")
	gone := released.provision(t, dropped)
	active := released.provision(t, functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:two"))
	if gone.Physical != active.Physical {
		t.Fatalf("the releases deployed %q and %q, want one Cloud Run service per function that every release revises", gone.Physical, active.Physical)
	}
	if _, err := p.Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}

	released.destroy(t, dropped.Ref)

	service := server.serving()
	if service == nil {
		t.Fatalf("reclaiming the dropped release deleted Cloud Run service %s, which the active release serves from", active.Physical)
	}
	if !servedBy(service.Traffic, active.Revision) {
		t.Errorf("the service serves %+v after the reclaim, want all of it still on the active release's %s", service.Traffic, active.Revision)
	}
	if revisions := server.remaining(); !slices.Equal(revisions, []string{active.Revision}) {
		t.Errorf("the service keeps revisions %v after the reclaim, want only %s: the dropped release's revision is its own to reclaim", revisions, active.Revision)
	}
}

func TestDestroyingEveryReleaseOfAFunctionDeletesItsServiceWhicheverGoesFirst(t *testing.T) {
	for name, activeFirst := range map[string]bool{"the active release first": true, "the dropped release first": false} {
		t.Run(name, func(t *testing.T) {
			server := &runServer{}
			p := server.open(t)
			released := newReleasedStacks(p)
			earlier := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one")
			later := functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:two")
			released.provision(t, earlier)
			active := released.provision(t, later)
			if _, err := p.Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
				t.Fatalf("Pin(%s) = %v", active.Revision, err)
			}

			order := []provider.StackRef{earlier.Ref, later.Ref}
			if activeFirst {
				order = []provider.StackRef{later.Ref, earlier.Ref}
			}
			for _, ref := range order {
				released.destroy(t, ref)
			}

			if service := server.serving(); service != nil {
				t.Errorf("destroying every release left Cloud Run service %s standing", active.Physical)
			}
		})
	}
}

func TestAContainerThatNamesNoHealthPathIsProbedAtTheRoot(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := previewSpec()
	spec.App.HealthCheckPath = ""

	if _, err := p.ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(server.created) == 0 {
		t.Fatal("ProvisionContainers() created no service")
	}
	container := server.created[len(server.created)-1].Template.Containers[0]
	if container.StartupProbe == nil || container.StartupProbe.HttpGet == nil || container.StartupProbe.HttpGet.Path != "/" {
		t.Errorf("a container naming no health path is probed at %+v, want /", container.StartupProbe)
	}
	for _, env := range container.Env {
		if env.Name == originguard.HealthPathVar && env.Value != "/" {
			t.Errorf("%s = %q, want /: the runtime answers the probe the service sends", originguard.HealthPathVar, env.Value)
		}
	}
}

func boundToAStore(spec provider.StackSpec) provider.StackSpec {
	spec.App.Values.Bindings = []provider.Binding{{Type: provider.BindingKV, Name: "cache"}}
	return spec
}

func TestAnAppBoundToADatabaseReachesPrivateRangesOverTheTiersSubnetwork(t *testing.T) {
	want := "PRIVATE_RANGES_ONLY projects/acme-prod/global/networks/ocel-preview projects/acme-prod/regions/europe-west1/subnetworks/ocel-preview"
	server := &runServer{}
	p := server.open(t)
	spec := previewSpec()
	spec.App.Values.Bindings = []provider.Binding{{Type: provider.BindingPostgres, Name: "orders"}}

	if _, err := p.ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if got := egressOf(server.created[len(server.created)-1]); got != want {
		t.Errorf("a service bound to a database has egress %q, want %q", got, want)
	}
}

func egressOf(service *run.GoogleCloudRunV2Service) string {
	access := service.Template.VpcAccess
	if access == nil || len(access.NetworkInterfaces) != 1 {
		return ""
	}
	return access.Egress + " " + access.NetworkInterfaces[0].Network + " " + access.NetworkInterfaces[0].Subnetwork
}

func TestAnAppBoundToAStoreReachesPrivateRangesOverTheTiersSubnetwork(t *testing.T) {
	want := "PRIVATE_RANGES_ONLY projects/acme-prod/global/networks/ocel-preview projects/acme-prod/regions/europe-west1/subnetworks/ocel-preview"
	server := &runServer{}
	p := server.open(t)

	if _, err := p.ProvisionContainers(context.Background(), boundToAStore(previewSpec()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if _, err := p.ProvisionFunctions(context.Background(), boundToAStore(previewSpec()), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	released := slices.Concat(server.created, server.patched)
	if len(released) != 2 {
		t.Fatalf("released %d services, want the container and the function", len(released))
	}
	for _, created := range released {
		if got := egressOf(created); got != want {
			t.Errorf("a service bound to a store has egress %q, want %q", got, want)
		}
	}
}

func TestAnAppBoundToNoStoreHasNoNetworkInterface(t *testing.T) {
	server := &runServer{}
	p := server.open(t)

	if _, err := p.ProvisionContainers(context.Background(), previewSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if got := egressOf(server.created[0]); got != "" {
		t.Errorf("a service bound to no store has egress %q, want none", got)
	}
}

func TestAnAppBoundToARealtimeIsToldWhereItsGatewayTakesPublishes(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := previewSpec()
	spec.App.Values.Bindings = []provider.Binding{{
		Type: provider.BindingRealtime, Name: "realtime--app", Resource: "app",
		Properties: map[string]string{provider.PropertyHost: "ocel-shop-pr-7-realtime-abc123-ew.a.run.app"},
	}}

	if _, err := p.ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	manifest, err := variables.Parse([]byte(envOf(server.created[0].Template.Containers[0])[variables.EnvVar]))
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://ocel-shop-pr-7-realtime-abc123-ew.a.run.app/publish"; manifest.RealtimePublishURL != want {
		t.Errorf("the manifest names %q as where to publish, want %q", manifest.RealtimePublishURL, want)
	}
}

func TestAnAppBoundToNoRealtimeIsToldOfNoGateway(t *testing.T) {
	server := &runServer{}
	p := server.open(t)

	if _, err := p.ProvisionContainers(context.Background(), boundToAStore(previewSpec()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	manifest, err := variables.Parse([]byte(envOf(server.created[0].Template.Containers[0])[variables.EnvVar]))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.RealtimePublishURL != "" {
		t.Errorf("the manifest names %q as where to publish, want nothing", manifest.RealtimePublishURL)
	}
}

func behindTheLoadBalancer(t *testing.T, p *Provider, spec provider.StackSpec) provider.StackSpec {
	t.Helper()
	front, err := p.Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec.Edge = front
	return spec
}

func TestEachReleaseBehindTheLoadBalancerTagsTheRevisionItCreatedWithItsReleaseToken(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	first := behindTheLoadBalancer(t, p, functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one"))
	one := released.provision(t, first)
	if _, err := p.Pin(context.Background(), one.Physical, one.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one.Revision, err)
	}
	second := behindTheLoadBalancer(t, p, functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:two"))
	two := released.provision(t, second)

	traffic := server.serving().Traffic
	for tag, revision := range map[string]string{first.Ref.Name.Release.String(): one.Revision, second.Ref.Name.Release.String(): two.Revision} {
		if !slices.ContainsFunc(traffic, func(target *run.GoogleCloudRunV2TrafficTarget) bool {
			return target.Tag == tag && revisionName(target.Revision) == revision
		}) {
			t.Errorf("the service carries %+v, want revision %s tagged %s: a deployment reaches the revision it released by that tag", traffic, revision, tag)
		}
	}
	if !servedBy(traffic, one.Revision) {
		t.Errorf("the service serves %+v after the second release tagged its revision, want all of it still on %s: a tag takes no traffic", traffic, one.Revision)
	}
}

func TestAReleaseWithNoEdgeInFrontTagsNoRevision(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	one := released.provision(t, functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:one"))
	if _, err := p.Pin(context.Background(), one.Physical, one.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", one.Revision, err)
	}
	released.provision(t, functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel/web-checkout@sha256:two"))

	for _, target := range server.serving().Traffic {
		if target.Tag != "" {
			t.Errorf("the service carries %+v, want no tag: with nothing in front, Cloud Run answers a tag on its own run.app url, "+
				"so a tag would publish each unpromoted revision to anyone once the service is open", server.serving().Traffic)
		}
	}
}

func TestPruningAReleaseUntagsTheImageNoRemainingRevisionRuns(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	dropped := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-one")
	released.provision(t, dropped)
	unchanged := functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-one")
	released.provision(t, unchanged)
	active := released.provision(t, functionRelease("d3", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-two"))
	if _, err := p.Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}

	released.destroy(t, dropped.Ref)
	if got := server.untags(); len(got) != 0 {
		t.Fatalf("pruning d1 untagged %v, want nothing: d2 still runs the same image", got)
	}
	released.destroy(t, unchanged.Ref)

	want := []string{"projects/acme/locations/europe-west1/repositories/ocel-preview/packages/web-checkout/tags/sha256-one"}
	if got := server.untags(); !slices.Equal(got, want) {
		t.Errorf("pruning the last release on sha256-one untagged %v, want %v: the repository's cleanup policy deletes an untagged image a week on, "+
			"and a tag no release runs keeps it forever", got, want)
	}
}

func TestDestroyingEveryReleaseOfAFunctionUntagsEveryImageItsRevisionsRan(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	only := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-one")
	released.provision(t, only)

	released.destroy(t, only.Ref)

	want := []string{"projects/acme/locations/europe-west1/repositories/ocel-preview/packages/web-checkout/tags/sha256-one"}
	if got := server.untags(); !slices.Equal(got, want) {
		t.Errorf("destroying the service untagged %v, want %v", got, want)
	}
}

func TestDestroyingAServiceLeavesTaggedAnImageAnotherServiceInTheRegionRuns(t *testing.T) {
	server := &runServer{elsewhere: map[string]string{
		"ocel-shop-prod-web-w-media-00001": "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-one",
	}}
	p := server.open(t)
	released := newReleasedStacks(p)
	only := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-one")
	released.provision(t, only)

	released.destroy(t, only.Ref)

	if got := server.untags(); len(got) != 0 {
		t.Errorf("destroying the service untagged %v, want nothing: a worker in the region still runs that image, "+
			"and the repository would delete it from under the worker a week on", got)
	}
}

func TestPruningAsksAfterEachImageByItsLabelAndListsNothingOfTheRegion(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	first := functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-one")
	one := released.provision(t, first)
	second := functionRelease("d2", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-two")
	two := released.provision(t, second)
	active := released.provision(t, functionRelease("d3", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-three"))
	if _, err := p.Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}

	if _, err := p.RemoveFunctionRevisions(context.Background(), first.Ref, []provider.Function{one, two}, nil, nil); err != nil {
		t.Fatalf("RemoveFunctionRevisions = %v", err)
	}

	if got := server.regionListings(); got != 0 {
		t.Errorf("pruning listed every revision in the region %d times, want none: that listing grows with every service in the account", got)
	}
	if got := server.labelQueries(); len(got) != 2 {
		t.Errorf("pruning asked %v, want one question per image it would untag, each naming the image's label", got)
	}
	if got := server.untags(); len(got) != 2 {
		t.Errorf("pruning untagged %v, want both images no remaining revision runs", got)
	}
}

func TestAReleaseTagsItsImageAgainWhenAPruneUntaggedItSinceThePush(t *testing.T) {
	image := "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-one"
	server := &runServer{missing: []string{"projects/acme/locations/europe-west1/repositories/ocel-preview/packages/web-checkout/tags/sha256-one"}}
	p := server.open(t)

	newReleasedStacks(p).provision(t, functionRelease("d1", image))

	want := []string{"projects/acme/locations/europe-west1/repositories/ocel-preview/packages/web-checkout/tags/sha256-one -> " +
		"projects/acme/locations/europe-west1/repositories/ocel-preview/packages/web-checkout/versions/sha256:one"}
	if got := server.retags(); !slices.Equal(got, want) {
		t.Errorf("the release tagged %v, want %v: a deploy that skipped its push because the tag was there hands Cloud Run a tag a prune may have taken since", got, want)
	}
}

func TestAReleaseLeavesATagThatIsStillThereAlone(t *testing.T) {
	server := &runServer{}
	p := server.open(t)

	newReleasedStacks(p).provision(t, functionRelease("d1", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-one"))

	if got := server.retags(); len(got) != 0 {
		t.Errorf("the release tagged %v, want nothing: the tag it hands Cloud Run is there", got)
	}
}

func TestPruningLeavesTaggedAnImageARemainingReleaseOfTheServiceRuns(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	released := newReleasedStacks(p)
	image := "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-one"
	first := functionRelease("d1", image)
	one := released.provision(t, first)
	second := functionRelease("d2", image)
	second.App.Functions[0].Env = map[string]string{"GREETING": "hello"}
	released.provision(t, second)
	active := released.provision(t, functionRelease("d3", "europe-west1-docker.pkg.dev/acme/ocel-preview/web-checkout:sha256-three"))
	if _, err := p.Pin(context.Background(), active.Physical, active.Revision, nil); err != nil {
		t.Fatalf("Pin(%s) = %v", active.Revision, err)
	}

	if _, err := p.RemoveFunctionRevisions(context.Background(), first.Ref, []provider.Function{one}, nil, nil); err != nil {
		t.Fatalf("RemoveFunctionRevisions = %v", err)
	}

	if got := server.untags(); len(got) != 0 {
		t.Errorf("pruning d1 untagged %v, want nothing: d2's revision runs the same image, and its release labels the revision with it", got)
	}
}
