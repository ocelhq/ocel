package gcp

import (
	"context"
	"testing"

	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
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
