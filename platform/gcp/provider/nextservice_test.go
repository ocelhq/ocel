package gcp

import (
	"context"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"

	run "google.golang.org/api/run/v2"

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
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

func nextSpec() provider.StackSpec {
	return provider.StackSpec{
		Ref: provider.StackRef{
			Project: "shop",
			Tier:    environment.TierProduction,
			Name:    naming.StackName{Env: stackrecords.ProductionEnv, App: "web"},
		},
		Kind: provider.StackApp,
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
				Image:     "europe-west1-docker.pkg.dev/acme/ocel/web@sha256:abc",
				Framework: buildoutput.Framework{Name: buildoutput.FrameworkNext, Arch: string(arch.X8664)},
			}},
		},
	}
}

func releasedNext(t *testing.T, spec provider.StackSpec) *run.GoogleCloudRunV2Container {
	t.Helper()
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if len(server.created) != 1 {
		t.Fatalf("released %d services, want the one service a Next app runs as", len(server.created))
	}
	return server.created[0].Template.Containers[0]
}

func TestANextServiceAsksForTwoGibibytesOfMemoryBilledPerInstance(t *testing.T) {
	container := releasedNext(t, nextSpec())

	if got := container.Resources.Limits["memory"]; got != "2048Mi" {
		t.Errorf("a Next service asks for %q of memory, want 2048Mi: rendering a page and resizing an image in one instance outgrows the profile a node function runs on", got)
	}
	if got := container.Resources.Limits["cpu"]; got != revisionCPU {
		t.Errorf("a Next service asks for %q CPU, want %q, which Cloud Run pairs with up to 4 GiB", got, revisionCPU)
	}
	if container.Resources.CpuIdle {
		t.Error("a Next service idles its CPU once a response ends, want it kept: work Next hands waitUntil runs after the response")
	}
}

func TestANextServiceCutsARequestOffAfterAMinute(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), nextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	if got := server.created[0].Template.Timeout; got != "60s" {
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
	if got := envOf(releasedNext(t, nextSpec()))[memoryEnvVar]; got != "2048" {
		t.Errorf("the Next runtime reads %s=%q, want 2048: Cloud Run names no memory to a container, and the runtime sizes its in-memory caches by it", memoryEnvVar, got)
	}
}

func TestANodeFunctionKeepsTheProfileItRunsOn(t *testing.T) {
	spec := nextSpec()
	spec.App.Framework = buildoutput.FrameworkNode
	spec.App.Functions[0].Framework.Name = buildoutput.FrameworkNode

	container := releasedNext(t, spec)
	if got := container.Resources.Limits["memory"]; got != revisionMemory {
		t.Errorf("a node function asks for %q of memory, want the profile's %q", got, revisionMemory)
	}
	if _, told := envOf(container)[memoryEnvVar]; told {
		t.Errorf("a node function is told %s, which only the Next runtime reads", memoryEnvVar)
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
	env := envOf(releasedNext(t, routedNextSpec()))

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
	env := envOf(releasedNext(t, routedNextSpec()))

	if got := env["OCEL_ISR_PREFIX"]; got != "prod/shop/web/r1/isr" {
		t.Errorf("the Next service reads OCEL_ISR_PREFIX=%q, want the prefix its spec names", got)
	}
	if got := env["OCEL_ISR_TAG_NAMESPACE"]; got != "PROJECT#shop#STACK#prod--web--r1#TAG#" {
		t.Errorf("the Next service reads OCEL_ISR_TAG_NAMESPACE=%q, want the namespace its spec names", got)
	}
}

func TestANextServiceIsToldTheCacheBucketAndTheObjectPrefixItsEntriesLiveUnder(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := envOf(server.created[0].Template.Containers[0])

	if got, want := env["OCEL_ISR_BUCKET"], names(t, p).Bucket(environment.TierProduction); got != want {
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
	env := envOf(releasedNext(t, nextSpec()))

	for _, name := range []string{"OCEL_ISR_BUCKET", "OCEL_ISR_OBJECT_PREFIX", "OCEL_STORAGE_ENDPOINT"} {
		if got, told := env[name]; told {
			t.Errorf("a Next service with no incremental cache reads %s=%q", name, got)
		}
	}
}

func TestANextServiceThatRoutesNothingIsToldNoRoutingManifest(t *testing.T) {
	env := envOf(releasedNext(t, nextSpec()))

	for _, name := range []string{"OCEL_NEXT_ROUTE_TABLE", "OCEL_ISR_PREFIX", "OCEL_ASSET_PREFIX"} {
		if got, told := env[name]; told {
			t.Errorf("a Next service whose spec routes nothing reads %s=%q", name, got)
		}
	}
}

func TestAGuardedNextServiceBehindAnEdgeThatShieldsNothingIsRefused(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
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
	if len(server.created) != 0 {
		t.Errorf("released %d services before refusing, want none", len(server.created))
	}
}

func TestANextServiceBehindAnEdgeThatRunsNoCodeRoutesItsOwnRequests(t *testing.T) {
	for _, kind := range []edge.Kind{edge.None, alb.Kind} {
		t.Run(string(kind), func(t *testing.T) {
			server := &runServer{}
			p := server.open(t)
			front, err := p.Edges().Open(kind, nil)
			if err != nil {
				t.Fatal(err)
			}
			spec := routedNextSpec()
			spec.Edge = front
			spec.App.Guard = &provider.OriginGuard{RootFunction: "bundle-0"}
			if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
				t.Fatalf("ProvisionFunctions() = %v", err)
			}

			env := envOf(server.created[0].Template.Containers[0])
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
	server := &runServer{}
	p := server.open(t)
	adoptBehindTheWorker(t, p, "https://writer.example.test", fake.KindRelay)
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
	if got, told := envOf(server.created[0].Template.Containers[0])[edge.OriginDispatchVar]; told {
		t.Errorf("a Next service behind an edge that routes reads %s=%q", edge.OriginDispatchVar, got)
	}
}

func TestAGuardedNextServiceIsReleasedWhereItsIngressKeepsClientsOffIt(t *testing.T) {
	for _, kind := range []edge.Kind{edge.None, alb.Kind} {
		t.Run(string(kind), func(t *testing.T) {
			server := &runServer{}
			p := server.open(t)
			front, err := p.Edges().Open(kind, nil)
			if err != nil {
				t.Fatal(err)
			}
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
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := envOf(server.created[0].Template.Containers[0])

	if got, want := env["OCEL_TAG_DATABASE"], "projects/"+names(t, p).project+"/databases/"+names(t, p).TagDatabase(environment.TierProduction); got != want {
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
	server := &runServer{}
	p := server.open(t)
	p.tagEndpoint = "http://127.0.0.1:8085"
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	tagEmulatorEnv(t, p, envOf(server.created[0].Template.Containers[0]))
}

func TestANextContainerReadsItsTagsFromTheFirestoreEmulatorWhenTheLaneRunsOne(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	p.tagEndpoint = "http://127.0.0.1:8085"
	if _, err := p.ProvisionContainers(context.Background(), nextContainerSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}

	tagEmulatorEnv(t, p, envOf(server.created[0].Template.Containers[0]))
}

func TestAnAppWithoutISRIsToldNoTagsDatabase(t *testing.T) {
	env := envOf(releasedNext(t, nextSpec()))

	for _, name := range []string{"OCEL_TAG_DATABASE", "OCEL_FIRESTORE_ENDPOINT"} {
		if got, told := env[name]; told {
			t.Errorf("a Next service with no incremental cache reads %s=%q", name, got)
		}
	}
}

var refreshEnvVars = []string{refreshURLEnvVar, refreshQueueEnvVar, refreshAccountEnvVar, refreshSecretEnvVar, tasksEndpointEnvVar, refreshTargetEnvVar, idTokenCertsURLEnvVar}

func TestANextServiceBilledPerRequestIsToldTheQueueItsRefreshesWaitInAndTheAccountTheyAreSignedAs(t *testing.T) {
	env := envOf(releasedNext(t, routedNextSpec()))
	derived := Names{namespace: "ocel", project: "acme-prod"}

	if got, want := env["OCEL_REFRESH_QUEUE"], "projects/acme-prod/locations/europe-west1/queues/ocel-production-delays"; got != want {
		t.Errorf("the Next service reads OCEL_REFRESH_QUEUE=%q, want %q", got, want)
	}
	if got, want := env["OCEL_REFRESH_ACCOUNT"], derived.RefreshAccountEmail(environment.TierProduction); got != want {
		t.Errorf("the Next service reads OCEL_REFRESH_ACCOUNT=%q, want %q", got, want)
	}
}

func TestANextServiceBilledPerRequestIsToldASecretToSignItsRefreshesWith(t *testing.T) {
	env := envOf(releasedNext(t, routedNextSpec()))

	if got := env[refreshSecretEnvVar]; len(got) < 26 {
		t.Errorf("the Next service reads %s=%q, want a secret of at least 26 characters", refreshSecretEnvVar, got)
	}
}

func TestEachReleaseOfANextServiceSignsItsRefreshesWithASecretOfItsOwn(t *testing.T) {
	first := envOf(releasedNext(t, routedNextSpec()))[refreshSecretEnvVar]
	second := envOf(releasedNext(t, routedNextSpec()))[refreshSecretEnvVar]

	if first == "" || first == second {
		t.Errorf("two releases read %s=%q and %q, want a secret apiece: a task one revision queued must not render on another", refreshSecretEnvVar, first, second)
	}
}

func TestANextServiceIsToldToReceiveRefreshesAtItsRunAppAddress(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := envOf(server.created[0].Template.Containers[0])

	want := "https://" + path.Base(server.service.Name) + "-123456789.europe-west1.run.app/_ocel/refresh"
	if got := env["OCEL_REFRESH_URL"]; got != want {
		t.Errorf("the Next service reads OCEL_REFRESH_URL=%q, want %q", got, want)
	}
}

func TestANextServiceBehindAnEdgeThatRunsCodeIsToldNoRefreshQueue(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	adoptBehindTheWorker(t, p, "https://writer.example.test", fake.KindRelay)
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

	env := envOf(server.created[0].Template.Containers[0])
	for _, name := range refreshEnvVars {
		if got, told := env[name]; told {
			t.Errorf("a Next service behind an edge that runs code reads %s=%q", name, got)
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

func releasedNextContainer(t *testing.T, spec provider.StackSpec) (*run.GoogleCloudRunV2Service, *runServer) {
	t.Helper()
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionContainers(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(server.created) == 0 {
		t.Fatal("ProvisionContainers() released no service")
	}
	return server.created[0], server
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
	if got := container.Resources.Limits["cpu"]; got != revisionCPU {
		t.Errorf("a Next container asks for %q CPU, want %q", got, revisionCPU)
	}
	if got := envOf(container)[memoryEnvVar]; got != "2048" {
		t.Errorf("the Next runtime reads %s=%q, want 2048", memoryEnvVar, got)
	}
}

func TestANextContainerIsToldTheCacheBucketObjectPrefixAndTagsDatabaseItsEntriesLiveIn(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionContainers(context.Background(), nextContainerSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	env := envOf(server.created[0].Template.Containers[0])

	for name, want := range map[string]string{
		"OCEL_ISR_BUCKET":        names(t, p).Bucket(environment.TierProduction),
		"OCEL_ISR_OBJECT_PREFIX": "cache/shop/web/prod/r1/isr",
		"OCEL_ISR_PREFIX":        "prod/shop/web/r1/isr",
		"OCEL_ISR_TAG_NAMESPACE": "PROJECT#shop#STACK#prod--web--r1#TAG#",
		"OCEL_TAG_DATABASE":      "projects/" + names(t, p).project + "/databases/" + names(t, p).TagDatabase(environment.TierProduction),
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

func TestAnEmulatedNextServiceQueuesItsRefreshesThroughTheEmulator(t *testing.T) {
	env := envOf(releasedNext(t, routedNextSpec()))

	endpoint, err := url.Parse(env["OCEL_TASKS_ENDPOINT"])
	if err != nil || endpoint.Hostname() != "host.docker.internal" {
		t.Errorf("the Next service reads OCEL_TASKS_ENDPOINT=%q, want an address on host.docker.internal", env["OCEL_TASKS_ENDPOINT"])
	}
}

func TestTheTasksEmulatorEndpointIsReadWhenTheProviderIsMade(t *testing.T) {
	t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", "http://127.0.0.1:4588")
	t.Setenv("OCEL_FLOCI_TASKS_ENDPOINT", "http://127.0.0.1:7001")

	p, err := NewProvider(Options{Project: "acme-prod", Region: "europe-west1"})
	if err != nil {
		t.Fatalf("NewProvider() = %v", err)
	}

	if p.tasksEndpoint != "http://127.0.0.1:7001" {
		t.Errorf("the provider addresses tasks at %q, want http://127.0.0.1:7001", p.tasksEndpoint)
	}
}

func TestANextServiceOnTheFlociLaneQueuesRefreshesAtTheTasksEmulatorAndChecksTokensWithItsKeys(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	p.tasksEndpoint = "http://127.0.0.1:7001"
	if _, err := p.ProvisionFunctions(context.Background(), routedNextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := envOf(server.created[0].Template.Containers[0])

	if got, want := env[tasksEndpointEnvVar], "http://host.docker.internal:7001"; got != want {
		t.Errorf("the Next service reads %s=%q, want %q", tasksEndpointEnvVar, got, want)
	}
	if got, want := env[idTokenCertsURLEnvVar], "http://host.docker.internal:7001/oauth2/v3/certs"; got != want {
		t.Errorf("the Next service reads %s=%q, want %q", idTokenCertsURLEnvVar, got, want)
	}
	if got := env[refreshURLEnvVar]; !strings.HasPrefix(got, "https://") || !strings.HasSuffix(got, "-123456789.europe-west1.run.app/_ocel/refresh") {
		t.Errorf("the Next service reads %s=%q, want the service's run.app url, which is the audience production signs for", refreshURLEnvVar, got)
	}
	if len(env[refreshSecretEnvVar]) < 26 {
		t.Errorf("the Next service reads a %d-character %s, want a per-deploy secret", len(env[refreshSecretEnvVar]), refreshSecretEnvVar)
	}
}

func TestAnEmulatedNextServiceWithNoTasksEmulatorIsToldNoTokenKeys(t *testing.T) {
	env := envOf(releasedNext(t, routedNextSpec()))

	if got, told := env[idTokenCertsURLEnvVar]; told {
		t.Errorf("a Next service on the floci lane with no tasks emulator reads %s=%q", idTokenCertsURLEnvVar, got)
	}
}

func TestAProviderAddressingGoogleNamesNoTokenKeysWhateverTasksEmulatorItHolds(t *testing.T) {
	p := &Provider{tasksEndpoint: "http://127.0.0.1:7001"}

	if got := p.containerTokenCertsURL(); got != "" {
		t.Errorf("a provider addressing Google names token keys at %q", got)
	}
}

func TestANextServiceOnGoogleCloudIsToldNoTasksEndpoint(t *testing.T) {
	env := newNextEnv(routedNextSpec(), routedNextSpec().App.Functions[0], serving{compute: provider.ComputeServerless}, nextCache{},
		&nextRefresh{url: "u", queue: "q", account: "a", secret: "s"})

	if got, told := env[tasksEndpointEnvVar]; told {
		t.Errorf("a Next service on Google Cloud reads %s=%q", tasksEndpointEnvVar, got)
	}
	if got, told := env[idTokenCertsURLEnvVar]; told {
		t.Errorf("a Next service on Google Cloud reads %s=%q", idTokenCertsURLEnvVar, got)
	}
	if env[refreshQueueEnvVar] != "q" {
		t.Errorf("the Next service reads %s=%q, want q", refreshQueueEnvVar, env[refreshQueueEnvVar])
	}
	if env[refreshSecretEnvVar] != "s" {
		t.Errorf("the Next service reads %s=%q, want s", refreshSecretEnvVar, env[refreshSecretEnvVar])
	}
}

func TestANodeFunctionIsToldNoRefreshQueue(t *testing.T) {
	spec := nextSpec()
	spec.App.Framework = buildoutput.FrameworkNode
	env := envOf(releasedNext(t, spec))

	for _, name := range refreshEnvVars {
		if got, told := env[name]; told {
			t.Errorf("a node function reads %s=%q", name, got)
		}
	}
}

func TestANextContainerIsToldNothingOnlyTheServerlessEntrypointReads(t *testing.T) {
	service, _ := releasedNextContainer(t, nextContainerSpec())

	env := envOf(service.Template.Containers[0])
	for _, name := range []string{
		routeTableEnvVar, routerKindEnvVar, assetBucketEnvVar,
		edge.OriginDispatchVar, edge.OriginSignedVar,
	} {
		if _, told := env[name]; told {
			t.Errorf("a Next container is told %s, which only ocel's serverless entrypoint reads", name)
		}
	}
}

func TestANextServiceOnContainerComputeIsToldNoRefreshQueue(t *testing.T) {
	spec := routedNextSpec()
	env := newNextEnv(spec, spec.App.Functions[0], serving{compute: provider.ComputeContainer}, nextCache{}, nil)

	for _, name := range refreshEnvVars {
		if got, told := env[name]; told {
			t.Errorf("a Next service on container compute reads %s=%q", name, got)
		}
	}
}

func TestAContainerThatServesNoNextKeepsItsProfileAndIsToldNoCacheLocation(t *testing.T) {
	spec := nextContainerSpec()
	spec.App.Framework = ""
	service, _ := releasedNextContainer(t, spec)

	container := service.Template.Containers[0]
	if got := container.Resources.Limits["memory"]; got != revisionMemory {
		t.Errorf("a container that serves no Next asks for %q of memory, want the profile's %q", got, revisionMemory)
	}
	for name := range envOf(container) {
		if strings.HasPrefix(name, "OCEL_ISR_") || name == tagDatabaseEnvVar || name == memoryEnvVar {
			t.Errorf("a container that serves no Next is told %s", name)
		}
	}
}

func TestANextServiceBilledPerRequestMakesTheRefreshURLOfItsServiceProjectNumberAndRegion(t *testing.T) {
	if got, want := refreshURLOf("ocel-shop-prod-web", 123456789, "europe-west1"), "https://ocel-shop-prod-web-123456789.europe-west1.run.app/_ocel/refresh"; got != want {
		t.Errorf("refreshURLOf() = %q, want %q", got, want)
	}
}

func TestANextServiceBehindTheLoadBalancerIsToldToSendRefreshesToItsRevisionTag(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := routedNextSpec()
	spec.Ref.Name = naming.AppStack(stackrecords.ProductionEnv, "web", naming.NewReleaseToken("d1", "f1"))
	spec = behindTheLoadBalancer(t, p, spec)
	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	env := envOf(server.created[0].Template.Containers[0])

	service := path.Base(server.service.Name)
	want := "https://" + spec.Ref.Name.Release.String() + "---" + service + "-123456789.europe-west1.run.app/_ocel/refresh"
	if got := env[refreshTargetEnvVar]; got != want {
		t.Errorf("the Next service reads %s=%q, want %q", refreshTargetEnvVar, got, want)
	}
	if got, want := env[refreshURLEnvVar], "https://"+service+"-123456789.europe-west1.run.app/_ocel/refresh"; got != want {
		t.Errorf("the Next service reads %s=%q, want the untagged service url %q: the token's audience stays the service url", refreshURLEnvVar, got, want)
	}
}

func TestANextServiceWithNoEdgeInFrontIsToldNoRefreshTarget(t *testing.T) {
	env := envOf(releasedNext(t, routedNextSpec()))

	if got, told := env[refreshTargetEnvVar]; told {
		t.Errorf("a Next service with no edge in front reads %s=%q", refreshTargetEnvVar, got)
	}
	if env[refreshURLEnvVar] == "" {
		t.Errorf("the Next service reads no %s", refreshURLEnvVar)
	}
}

func TestARefreshTagURLPutsTheTagBeforeTheServiceProjectNumberAndRegion(t *testing.T) {
	if got, want := refreshTagURLOf("r0000000a", "ocel-shop-prod-web", 123456789, "europe-west1"), "https://r0000000a---ocel-shop-prod-web-123456789.europe-west1.run.app/_ocel/refresh"; got != want {
		t.Errorf("refreshTagURLOf() = %q, want %q", got, want)
	}
}

func TestARevisionTagTooLongForTheRunAppLabelGivesNoRefreshTagURL(t *testing.T) {
	const tag = "r0000000a"
	fitting := strings.Repeat("s", 63-len(tag+"---"+"-123456789"))
	if got := refreshTagURLOf(tag, fitting, 123456789, "europe-west1"); got == "" {
		t.Errorf("refreshTagURLOf() of a 63-character label = %q, want a url", got)
	}
	if got := refreshTagURLOf(tag, fitting+"s", 123456789, "europe-west1"); got != "" {
		t.Errorf("refreshTagURLOf() of a 64-character label = %q, want none", got)
	}
}

func TestANextPreviewBehindIdentityAwareProxyRefreshesOnItsOwnRatherThanByTask(t *testing.T) {
	if refreshesByTask(buildoutput.FrameworkNext, provider.ComputeServerless, edge.Facts{}, true) {
		t.Error("a Next preview behind Identity-Aware Proxy refreshes by task, but Cloud Tasks' token cannot pass the proxy")
	}
	if !refreshesByTask(buildoutput.FrameworkNext, provider.ComputeServerless, edge.Facts{}, false) {
		t.Error("a Next app that is not gated refreshes on its own, want the tier's queue")
	}
}

func TestANextServiceGivenNoRefreshQueueIsToldNone(t *testing.T) {
	spec := routedNextSpec()

	env := newNextEnv(spec, spec.App.Functions[0], serving{compute: provider.ComputeServerless}, nextCache{}, nil)
	for _, name := range refreshEnvVars {
		if got, told := env[name]; told {
			t.Errorf("a Next service given no refresh queue reads %s=%q", name, got)
		}
	}
}

func TestAWorkerOfANextContainerIsToldNoCacheLocation(t *testing.T) {
	spec := nextContainerSpec()
	spec.App.Workers = []provider.WorkerSpec{{Name: "media"}}
	_, server := releasedNextContainer(t, spec)

	workers := slices.Concat(server.created[1:], server.releases())
	if len(workers) == 0 {
		t.Fatal("the app's worker was never released")
	}
	for _, worker := range workers {
		if worker.Template == nil {
			continue
		}
		for name := range envOf(worker.Template.Containers[0]) {
			if strings.HasPrefix(name, "OCEL_ISR_") || name == tagDatabaseEnvVar || name == memoryEnvVar {
				t.Errorf("a worker of a Next container is told %s, which only the Next service reads", name)
			}
		}
	}
}

func TestANextServiceThatRoutesItsOwnRequestsIsToldTheStaticRulesItsBuildStates(t *testing.T) {
	spec := routedNextSpec()
	spec.App.Static = &edge.Static{ImmutablePrefixes: []string{"/docs/_next/static/"}}
	env := envOf(releasedNext(t, spec))

	if got, want := env[edge.StaticRulesVar], `{"immutablePrefixes":["/docs/_next/static/"]}`; got != want {
		t.Errorf("the Next service reads %s=%q, want %q", edge.StaticRulesVar, got, want)
	}
}
