package gcp

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"

	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func nextSpec() provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Tier:    environment.TierProduction,
			Name:    naming.StackName{Env: stackrecords.ProductionEnv, App: "web"},
		},
		Kind:   provider.StackApp,
		Images: provider.ImagePushes{Store: alreadyPushed{}, Pushes: []provider.ImagePush{{App: "bundle-0", ImageRef: "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc", Function: true}}},
		App: &provider.AppSpec{
			App:          "web",
			Framework:    buildoutput.FrameworkNext,
			RootFunction: "bundle-0",
			BuildID:      "dpl_7",
			Compute:      provider.ComputeServerless,
			Router:       "cloudrun",
			Functions: []provider.FunctionSpec{{
				Name:      "bundle-0",
				Route:     "bundle-0",
				Framework: buildoutput.Framework{Name: buildoutput.FrameworkNext, Arch: string(arch.X8664)},
			}},
		},
	}
}

func releasedNext(t *testing.T, spec provider.StackSpec) *runv2.GoogleCloudRunV2Container {
	t.Helper()
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if len(server.Created) != 1 {
		t.Fatalf("released %d services, want the one service a Next app runs as", len(server.Created))
	}
	return server.Created[0].Template.Containers[0]
}

func TestANextServiceAsksForTwoGibibytesOfMemoryBilledPerInstance(t *testing.T) {
	container := releasedNext(t, nextSpec())

	if got := container.Resources.Limits["memory"]; got != "2048Mi" {
		t.Errorf("a Next service asks for %q of memory, want 2048Mi: rendering a page and resizing an image in one instance outgrows the profile a node function runs on", got)
	}
	if got := container.Resources.Limits["cpu"]; got != run.RevisionCPU {
		t.Errorf("a Next service asks for %q CPU, want %q, which Cloud Run pairs with up to 4 GiB", got, run.RevisionCPU)
	}
	if container.Resources.CpuIdle {
		t.Error("a Next service idles its CPU once a response ends, want it kept: work Next hands waitUntil runs after the response")
	}
}

func TestANextServiceCutsARequestOffAfterAMinute(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionFunctions(context.Background(), nextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := server.Created[0].Template.Timeout; got != "60s" {
		t.Errorf("a Next request is cut off after %q, want 60s, as long as a Next request runs on Lambda", got)
	}
}

func TestANextServiceKeepsTheMemoryItsSpecNamed(t *testing.T) {
	spec := nextSpec()
	spec.App.Functions[0].Memory = 4096

	if got := releasedNext(t, spec).Resources.Limits["memory"]; got != "4096Mi" {
		t.Errorf("a Next service asks for %q of memory, want the 4096Mi its spec named", got)
	}
}

func TestTheNextRuntimeIsToldTheMemoryItsServiceRunsWith(t *testing.T) {
	if got := googletest.EnvOf(releasedNext(t, nextSpec()))[next.MemoryEnvVar]; got != "2048" {
		t.Errorf("the Next runtime reads %s=%q, want 2048: Cloud Run names no memory to a container, and the runtime sizes its in-memory caches by it", next.MemoryEnvVar, got)
	}
}

func TestANodeFunctionKeepsTheProfileItRunsOn(t *testing.T) {
	spec := nextSpec()
	spec.App.Framework = buildoutput.FrameworkNode
	spec.App.Functions[0].Framework.Name = buildoutput.FrameworkNode

	container := releasedNext(t, spec)
	if got := container.Resources.Limits["memory"]; got != run.RevisionMemory {
		t.Errorf("a node function asks for %q of memory, want the profile's %q", got, run.RevisionMemory)
	}
	if _, told := googletest.EnvOf(container)[next.MemoryEnvVar]; told {
		t.Errorf("a node function is told %s, which only the Next runtime reads", next.MemoryEnvVar)
	}
}

func routedNextSpec() provider.StackSpec {
	spec := nextSpec()
	spec.App.Routing = &provider.RoutingSpec{RootFunction: "bundle-0", RouteTable: router.RouteTable{Format: edge.RouteTableNext, Table: []byte(`{"rootFunction":"bundle-0"}`)}}
	spec.App.ISR = &provider.ISRSpec{Prefix: "prod/shop/web/r1/isr", TagNamespace: "PROJECT#shop#STACK#prod--web--r1#TAG#"}
	spec.App.AssetPrefix = "prod/shop/web/r1/assets"
	return spec
}

func TestANextServiceThatRoutesItsOwnRequestsIsToldWhatItRoutesBy(t *testing.T) {
	env := googletest.EnvOf(releasedNext(t, routedNextSpec()))

	for name, want := range map[string]string{
		"OCEL_NEXT_ROUTE_TABLE": "/ocel/app/" + edge.NextRouteTableFile,
		"OCEL_ASSET_PREFIX":     "prod/shop/web/r1/assets",
		"OCEL_SLUG":             "shop",
		"OCEL_APP":              "web",
		"OCEL_BUILD_ID":         "dpl_7",
		"OCEL_ROUTER_KIND":      "cloudrun",
	} {
		if got := env[name]; got != want {
			t.Errorf("the Next service reads %s=%q, want %q", name, got, want)
		}
	}
}

func TestANextServiceIsToldWhereItsIncrementalCacheLives(t *testing.T) {
	env := googletest.EnvOf(releasedNext(t, routedNextSpec()))

	if got := env["OCEL_ISR_PREFIX"]; got != "prod/shop/web/r1/isr" {
		t.Errorf("the Next service reads OCEL_ISR_PREFIX=%q, want the prefix its spec names", got)
	}
	if got := env["OCEL_ISR_TAG_NAMESPACE"]; got != "PROJECT#shop#STACK#prod--web--r1#TAG#" {
		t.Errorf("the Next service reads OCEL_ISR_TAG_NAMESPACE=%q, want the namespace its spec names", got)
	}
}

func TestANextServiceIsToldTheCacheBucketAndTheObjectPrefixItsEntriesLiveUnder(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := googletest.EnvOf(server.Created[0].Template.Containers[0])

	if got, want := env["OCEL_ISR_BUCKET"], namesOf(t, p).Bucket(environment.TierProduction); got != want {
		t.Errorf("the Next service reads OCEL_ISR_BUCKET=%q, want its tier's bucket %q", got, want)
	}
	if got, want := env["OCEL_ISR_OBJECT_PREFIX"], "cache/shop/web/prod/r1/isr"; got != want {
		t.Errorf("the Next service reads OCEL_ISR_OBJECT_PREFIX=%q, want %q", got, want)
	}
	endpoint, err := url.Parse(env["OCEL_STORAGE_ENDPOINT"])
	if err != nil || endpoint.Hostname() != "host.docker.internal" {
		t.Errorf("the Next service reads OCEL_STORAGE_ENDPOINT=%q, want an address on host.docker.internal", env["OCEL_STORAGE_ENDPOINT"])
	}
}

func TestANextServiceWithoutAnIncrementalCacheIsToldNoCacheLocation(t *testing.T) {
	env := googletest.EnvOf(releasedNext(t, nextSpec()))

	for _, name := range []string{"OCEL_ISR_BUCKET", "OCEL_ISR_OBJECT_PREFIX", "OCEL_STORAGE_ENDPOINT"} {
		if got, told := env[name]; told {
			t.Errorf("a Next service with no incremental cache reads %s=%q", name, got)
		}
	}
}

func TestANextServiceThatRoutesNothingIsToldNoRoutingManifest(t *testing.T) {
	env := googletest.EnvOf(releasedNext(t, nextSpec()))

	for _, name := range []string{"OCEL_NEXT_ROUTE_TABLE", "OCEL_ISR_PREFIX", "OCEL_ASSET_PREFIX"} {
		if got, told := env[name]; told {
			t.Errorf("a Next service whose spec routes nothing reads %s=%q", name, got)
		}
	}
}

func TestAGuardedNextServiceBehindAnEdgeThatShieldsNothingIsRefused(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	front, err := fake.NewEdges().Open(fake.KindDirect, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := routedNextSpec()
	spec.Edge = front
	spec.App.Guard = &provider.OriginGuard{RootFunction: "bundle-0"}

	_, err = p.ProvisionFunctions(context.Background(), spec, nil)
	if err == nil {
		t.Fatal("ProvisionFunctions() released a guarded Next service anyone could reach around its edge, want it refused")
	}
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Errorf("ProvisionFunctions() code = %v, want %v", code, refusal.CodeInvalid)
	}
	if len(server.Created) != 0 {
		t.Errorf("released %d services before refusing, want none", len(server.Created))
	}
}

func TestANextServiceBehindAnEdgeThatRunsNoCodeRoutesItsOwnRequests(t *testing.T) {
	for _, kind := range []edge.Kind{edge.None, alb.Kind} {
		t.Run(string(kind), func(t *testing.T) {
			server := &googletest.Run{}
			p := providerServing(server, t)
			front, err := p.Edges().Open(kind, nil)
			if err != nil {
				t.Fatal(err)
			}
			publishGateOn(t, p, environment.TierProduction, nil)
			spec := routedNextSpec()
			spec.Edge = front
			spec.App.Guard = &provider.OriginGuard{RootFunction: "bundle-0"}
			if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
				t.Fatalf("ProvisionFunctions() = %v", err)
			}

			env := googletest.EnvOf(server.Created[0].Template.Containers[0])
			if env[edge.OriginDispatchVar] != "1" {
				t.Errorf("the Next service reads %s=%q, want 1: nothing in front of it routes a request to a route", edge.OriginDispatchVar, env[edge.OriginDispatchVar])
			}
			if env[edge.OriginSignedVar] != "1" {
				t.Errorf("the Next service reads %s=%q, want 1: its ingress keeps clients from going around its edge, and it holds no secret a forward could present", edge.OriginSignedVar, env[edge.OriginSignedVar])
			}
		})
	}
}

func TestANextServiceBehindAnEdgeThatRunsCodeLeavesRoutingToIt(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	adoptBehindTheWorker(t, p, fake.KindRelay)
	front, err := fake.NewEdges().Open(fake.KindRelay, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := routedNextSpec()
	spec.Edge = shieldingFront{front}
	spec.Ref.Name.Release = naming.NewReleaseToken("d1", "f1")

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if got, told := googletest.EnvOf(server.Created[0].Template.Containers[0])[edge.OriginDispatchVar]; told {
		t.Errorf("a Next service behind an edge that routes reads %s=%q", edge.OriginDispatchVar, got)
	}
}

func TestAGuardedNextServiceIsReleasedWhereItsIngressKeepsClientsOffIt(t *testing.T) {
	for _, kind := range []edge.Kind{edge.None, alb.Kind} {
		t.Run(string(kind), func(t *testing.T) {
			server := &googletest.Run{}
			p := providerServing(server, t)
			front, err := p.Edges().Open(kind, nil)
			if err != nil {
				t.Fatal(err)
			}
			publishGateOn(t, p, environment.TierProduction, nil)
			spec := routedNextSpec()
			spec.Edge = front
			spec.App.Guard = &provider.OriginGuard{RootFunction: "bundle-0"}

			if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
				t.Fatalf("ProvisionFunctions() = %v", err)
			}
		})
	}
}

func TestANextAppIsToldTheTagsDatabaseItsRecordsLiveIn(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := googletest.EnvOf(server.Created[0].Template.Containers[0])

	if got, want := env["OCEL_TAG_DATABASE"], "projects/"+namesOf(t, p).Project()+"/databases/"+namesOf(t, p).TagDatabase(environment.TierProduction); got != want {
		t.Errorf("the Next service reads OCEL_TAG_DATABASE=%q, want %q", got, want)
	}
	endpoint, err := url.Parse(env["OCEL_FIRESTORE_ENDPOINT"])
	if err != nil || endpoint.Hostname() != "host.docker.internal" {
		t.Errorf("the Next service reads OCEL_FIRESTORE_ENDPOINT=%q, want an address on host.docker.internal", env["OCEL_FIRESTORE_ENDPOINT"])
	}
}

func tagEmulatorEnv(t *testing.T, p *Provider, env map[string]string) {
	t.Helper()
	tags, err := url.Parse(env["OCEL_FIRESTORE_ENDPOINT"])
	if err != nil || tags.Hostname() != "host.docker.internal" || tags.Port() != "8085" {
		t.Errorf("the Next service reads OCEL_FIRESTORE_ENDPOINT=%q, want host.docker.internal:8085", env["OCEL_FIRESTORE_ENDPOINT"])
	}
	storage, err := url.Parse(env["OCEL_STORAGE_ENDPOINT"])
	floci, _ := url.Parse(p.endpoint)
	if err != nil || storage.Hostname() != "host.docker.internal" || storage.Port() != floci.Port() {
		t.Errorf("the Next service reads OCEL_STORAGE_ENDPOINT=%q, want host.docker.internal:%s", env["OCEL_STORAGE_ENDPOINT"], floci.Port())
	}
}

func TestANextServiceReadsItsTagsFromTheFirestoreEmulatorWhenTheLaneRunsOne(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	p.tagEndpoint = "http://127.0.0.1:8085"
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	tagEmulatorEnv(t, p, googletest.EnvOf(server.Created[0].Template.Containers[0]))
}

func TestANextContainerReadsItsTagsFromTheFirestoreEmulatorWhenTheLaneRunsOne(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	p.tagEndpoint = "http://127.0.0.1:8085"
	if _, err := p.ProvisionContainers(context.Background(), nextContainerSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	tagEmulatorEnv(t, p, googletest.EnvOf(server.Created[0].Template.Containers[0]))
}

func TestAnAppWithoutISRIsToldNoTagsDatabase(t *testing.T) {
	env := googletest.EnvOf(releasedNext(t, nextSpec()))

	for _, name := range []string{"OCEL_TAG_DATABASE", "OCEL_FIRESTORE_ENDPOINT"} {
		if got, told := env[name]; told {
			t.Errorf("a Next service with no incremental cache reads %s=%q", name, got)
		}
	}
}

func nextContainerSpec() provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Tier:    environment.TierProduction,
			Name:    naming.StackName{Env: stackrecords.ProductionEnv, App: "web"},
		},
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:             "web",
			Framework:       buildoutput.FrameworkNext,
			Compute:         provider.ComputeContainer,
			Image:           "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
			HealthCheckPath: "/",
			Instances:       provider.Instances{Min: 1, Max: 1},
			ISR:             &provider.ISRSpec{Prefix: "prod/shop/web/r1/isr", TagNamespace: "PROJECT#shop#STACK#prod--web--r1#TAG#"},
		},
	}
}

func releasedNextContainer(t *testing.T, spec provider.StackSpec) (*runv2.GoogleCloudRunV2Service, *googletest.Run) {
	t.Helper()
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(server.Created) == 0 {
		t.Fatal("ProvisionContainers() released no service")
	}
	return server.Created[0], server
}

func TestANextContainerKeepsItsCPUBetweenRequestsSoNextRefreshesInTheBackground(t *testing.T) {
	service, _ := releasedNextContainer(t, nextContainerSpec())

	if service.Template.Containers[0].Resources.CpuIdle {
		t.Error("a Next container releases its CPU between requests, and Next revalidates stale pages in the background after the response")
	}
}

func TestANextContainerKeepsAnInstanceRunningUnlessItsAppAsksForNone(t *testing.T) {
	for min, want := range map[int]int64{1: 1, 0: 0} {
		spec := nextContainerSpec()
		spec.App.Instances = provider.Instances{Min: min, Max: 1}
		service, _ := releasedNextContainer(t, spec)

		if got := service.Template.Scaling.MinInstanceCount; got != want {
			t.Errorf("an app asking for %d minimum instances runs %d, want %d", min, got, want)
		}
	}
}

func TestANextContainerAsksForTwoGibibytesOfMemory(t *testing.T) {
	service, _ := releasedNextContainer(t, nextContainerSpec())

	container := service.Template.Containers[0]
	if got := container.Resources.Limits["memory"]; got != "2048Mi" {
		t.Errorf("a Next container asks for %q of memory, want 2048Mi, as a serverless Next service does", got)
	}
	if got := container.Resources.Limits["cpu"]; got != run.RevisionCPU {
		t.Errorf("a Next container asks for %q CPU, want %q", got, run.RevisionCPU)
	}
	if got := googletest.EnvOf(container)[next.MemoryEnvVar]; got != "2048" {
		t.Errorf("the Next runtime reads %s=%q, want 2048", next.MemoryEnvVar, got)
	}
}

func TestANextContainerIsToldTheCacheBucketObjectPrefixAndTagsDatabaseItsEntriesLiveIn(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	if _, err := p.ProvisionContainers(context.Background(), nextContainerSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	env := googletest.EnvOf(server.Created[0].Template.Containers[0])

	for name, want := range map[string]string{
		"OCEL_ISR_BUCKET":        namesOf(t, p).Bucket(environment.TierProduction),
		"OCEL_ISR_OBJECT_PREFIX": "cache/shop/web/prod/r1/isr",
		"OCEL_ISR_PREFIX":        "prod/shop/web/r1/isr",
		"OCEL_ISR_TAG_NAMESPACE": "PROJECT#shop#STACK#prod--web--r1#TAG#",
		"OCEL_TAG_DATABASE":      "projects/" + namesOf(t, p).Project() + "/databases/" + namesOf(t, p).TagDatabase(environment.TierProduction),
	} {
		if got := env[name]; got != want {
			t.Errorf("a Next container reads %s=%q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"OCEL_STORAGE_ENDPOINT", "OCEL_FIRESTORE_ENDPOINT"} {
		if env[name] == "" {
			t.Errorf("an emulated Next container reads no %s", name)
		}
	}
}

func TestANextContainerIsToldNothingOnlyTheServerlessEntrypointReads(t *testing.T) {
	service, _ := releasedNextContainer(t, nextContainerSpec())

	env := googletest.EnvOf(service.Template.Containers[0])
	for _, name := range []string{
		next.RouteTableEnvVar, next.RouterKindEnvVar, next.AssetBucketEnvVar,
		edge.OriginDispatchVar, edge.OriginSignedVar,
	} {
		if _, told := env[name]; told {
			t.Errorf("a Next container is told %s, which only ocel's serverless entrypoint reads", name)
		}
	}
}

func TestAContainerThatServesNoNextKeepsItsProfileAndIsToldNoCacheLocation(t *testing.T) {
	spec := nextContainerSpec()
	spec.App.Framework = ""
	service, _ := releasedNextContainer(t, spec)

	container := service.Template.Containers[0]
	if got := container.Resources.Limits["memory"]; got != run.RevisionMemory {
		t.Errorf("a container that serves no Next asks for %q of memory, want the profile's %q", got, run.RevisionMemory)
	}
	for name := range googletest.EnvOf(container) {
		if strings.HasPrefix(name, "OCEL_ISR_") || name == next.TagDatabaseEnvVar || name == next.MemoryEnvVar {
			t.Errorf("a container that serves no Next is told %s", name)
		}
	}
}

func TestAWorkerOfANextContainerIsToldNoCacheLocation(t *testing.T) {
	spec := nextContainerSpec()
	spec.App.Workers = []provider.WorkerSpec{{Name: "media"}}
	_, server := releasedNextContainer(t, spec)

	workers := slices.Concat(server.Created[1:], server.Releases())
	if len(workers) == 0 {
		t.Fatal("the app's worker was never released")
	}
	for _, worker := range workers {
		if worker.Template == nil {
			continue
		}
		for name := range googletest.EnvOf(worker.Template.Containers[0]) {
			if strings.HasPrefix(name, "OCEL_ISR_") || name == next.TagDatabaseEnvVar || name == next.MemoryEnvVar {
				t.Errorf("a worker of a Next container is told %s, which only the Next service reads", name)
			}
		}
	}
}

func TestANextServiceThatRoutesItsOwnRequestsIsToldTheStaticRulesItsBuildStates(t *testing.T) {
	spec := routedNextSpec()
	spec.App.Static = &edge.Static{ImmutablePrefixes: []string{"/docs/_next/static/"}}
	env := googletest.EnvOf(releasedNext(t, spec))

	if got, want := env[edge.StaticRulesVar], `{"immutablePrefixes":["/docs/_next/static/"]}`; got != want {
		t.Errorf("the Next service reads %s=%q, want %q", edge.StaticRulesVar, got, want)
	}
}
