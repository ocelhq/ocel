package gcp

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func asNodeThatRevalidates(spec provider.StackSpec) provider.StackSpec {
	spec.App.Framework = buildoutput.FrameworkNode
	spec.App.Functions[0].Framework.Name = buildoutput.FrameworkNode
	spec.App.Routing = nil
	return spec
}

func TestANodeServiceThatRevalidatesBehindTheALBIsToldWhichCloudCDNToClear(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	spec := asNodeThatRevalidates(behindTheALB(t, p))

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	env := envOf(server.Created[0].Template.Containers[0])
	if got, want := env["OCEL_CDN_URL_MAP"], "projects/acme-prod/global/urlMaps/ocel-alb-production-routes"; got != want {
		t.Errorf("the node service reads OCEL_CDN_URL_MAP=%q, want %q", got, want)
	}
	if got, want := env["OCEL_ISR_PREFIX"], spec.App.ISR.Prefix; got != want {
		t.Errorf("the node service reads OCEL_ISR_PREFIX=%q, want %q: the release its tags are stamped with comes from it", got, want)
	}
	if bound := purgeBindingsOf(server.Identities()); len(bound) == 0 {
		t.Error("the node service's account was granted no cache purge role, so its revalidations are refused by Cloud CDN")
	}
	for _, name := range []string{"OCEL_TAG_DATABASE", "OCEL_ISR_BUCKET", "OCEL_ISR_OBJECT_PREFIX"} {
		if got, told := env[name]; told {
			t.Errorf("the node service reads %s=%q, which only Next's incremental cache reads", name, got)
		}
	}
}

func TestANodeServiceThatRevalidatesBehindTheWorkerIsToldWhereToPurgeAndTheSecretToSignWith(t *testing.T) {
	w := behindTheWorkerWithItsSeed(t)
	w.spec = asNodeThatRevalidates(w.spec)
	w.spec.App.ISR.PurgeURL = "https://shop.acct.workers.dev/purge"

	if _, err := w.p.ProvisionFunctions(context.Background(), w.spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	env := envOf(w.server.Created[0].Template.Containers[0])
	if got := env[edge.PurgeURLVar]; got != "https://shop.acct.workers.dev/purge" {
		t.Errorf("%s = %q, want the URL the worker takes purges at", edge.PurgeURLVar, got)
	}
	if want := edge.DerivePurgeSecret(w.seed, w.spec.App.ISR.Prefix); env["OCEL_PURGE_SECRET"] != want {
		t.Errorf("OCEL_PURGE_SECRET = %q, want the deployment's derived secret", env["OCEL_PURGE_SECRET"])
	}
	if env["OCEL_ISR_PREFIX"] != w.spec.App.ISR.Prefix {
		t.Errorf("OCEL_ISR_PREFIX = %q, want %q", env["OCEL_ISR_PREFIX"], w.spec.App.ISR.Prefix)
	}
	if got, told := env["OCEL_TAG_DATABASE"]; told {
		t.Errorf("OCEL_TAG_DATABASE = %q, which only Next's incremental cache reads", got)
	}
}

func TestANodeServiceThatRevalidatesNothingIsToldNoEdgeToPurge(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	spec := asNodeThatRevalidates(behindTheALB(t, p))
	spec.App.ISR = nil

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	env := envOf(server.Created[0].Template.Containers[0])
	for _, name := range []string{"OCEL_CDN_URL_MAP", "OCEL_ISR_PREFIX", "OCEL_PURGE_URL", "OCEL_PURGE_SECRET"} {
		if got, told := env[name]; told {
			t.Errorf("the node service reads %s=%q, want none", name, got)
		}
	}
	if bound := purgeBindingsOf(server.Identities()); len(bound) != 0 {
		t.Errorf("the node service's account was granted %q, want no purge binding", bound)
	}
}
