package gcp

import (
	"context"
	"strconv"
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
			App:        "web",
			Framework:  buildoutput.FrameworkNext,
			Entry:      "bundle-0",
			Deployment: "dpl_7",
			Compute:    provider.ComputeServerless,
			Router:     "cloudrun",
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

func TestANextServiceAsksForTwoGibibytesOfMemoryBilledPerRequest(t *testing.T) {
	container := releasedNext(t, nextSpec())

	if got := container.Resources.Limits["memory"]; got != "2048Mi" {
		t.Errorf("a Next service asks for %q of memory, want 2048Mi: rendering a page and resizing an image in one instance outgrows the profile a node function runs on", got)
	}
	if got := container.Resources.Limits["cpu"]; got != revisionCPU {
		t.Errorf("a Next service asks for %q CPU, want %q, which Cloud Run pairs with up to 4 GiB", got, revisionCPU)
	}
	if !container.Resources.CpuIdle {
		t.Error("a Next service keeps its CPU between requests, and a serverless app is billed per request")
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
	spec.App.Routing = &provider.RoutingSpec{Entry: "bundle-0", Manifest: []byte(`{"entry":"bundle-0"}`)}
	spec.App.ISR = &provider.ISRSpec{Prefix: "prod/shop/web/r1/isr", TagNamespace: "PROJECT#shop#STACK#prod--web--r1#TAG#"}
	spec.App.AssetPrefix = "prod/shop/web/r1/assets"
	return spec
}

func TestANextServiceThatRoutesItsOwnRequestsIsToldWhatItRoutesBy(t *testing.T) {
	env := envOf(releasedNext(t, routedNextSpec()))

	for name, want := range map[string]string{
		"OCEL_ROUTING_MANIFEST": "/ocel/app/" + edge.RoutingManifestFile,
		"OCEL_ASSET_PREFIX":     "prod/shop/web/r1/assets",
		"OCEL_SLUG":             "shop",
		"OCEL_APP":              "web",
		"OCEL_DEPLOYMENT_ID":    "dpl_7",
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

func TestANextServiceThatRoutesNothingIsToldNoRoutingManifest(t *testing.T) {
	env := envOf(releasedNext(t, nextSpec()))

	for _, name := range []string{"OCEL_ROUTING_MANIFEST", "OCEL_ISR_PREFIX", "OCEL_ASSET_PREFIX"} {
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
	spec.App.Guard = &provider.OriginGuard{Entry: "bundle-0"}

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
			spec.App.Guard = &provider.OriginGuard{Entry: "bundle-0"}
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
	front, err := fake.NewEdges().Open(fake.KindRelay, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := routedNextSpec()
	spec.Edge = front

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if got, told := envOf(server.created[0].Template.Containers[0])[edge.OriginDispatchVar]; told {
		t.Errorf("a Next service behind an edge that routes reads %s=%q", edge.OriginDispatchVar, got)
	}
}

func TestANextServiceBilledPerRequestFinishesItsWorkBeforeItsResponseEnds(t *testing.T) {
	container := releasedNext(t, routedNextSpec())

	capMs, err := strconv.Atoi(envOf(container)[finishBeforeResponseEnvVar])
	if err != nil || capMs <= 0 {
		t.Fatalf("the Next service reads %s=%q, want a cap in milliseconds: Cloud Run takes an instance's CPU away once a request billed per request is answered",
			finishBeforeResponseEnvVar, envOf(container)[finishBeforeResponseEnvVar])
	}
	if limit := int(nextRequestTimeout.Milliseconds()); capMs >= limit {
		t.Errorf("the Next service holds its response's end for up to %dms, want well under the %dms Cloud Run lets a request run", capMs, limit)
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
			spec.App.Guard = &provider.OriginGuard{Entry: "bundle-0"}

			if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
				t.Fatalf("ProvisionFunctions() = %v", err)
			}
		})
	}
}
