package gcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const cdnPurgeRolePath = "projects/acme-prod/roles/ocel_cdn_purge"

func behindTheALB(t *testing.T, p *Provider) provider.StackSpec {
	t.Helper()
	front, err := p.Edges().Open(alb.Kind, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := routedNextSpec()
	spec.Edge = front
	spec.App.Guard = &provider.OriginGuard{Entry: "bundle-0"}
	return spec
}

func purgeBindingsOf(server *iamServer) []string {
	var bound []string
	for _, binding := range projectBindingsOf(server) {
		if strings.HasPrefix(binding, cdnPurgeRolePath+" ") {
			bound = append(bound, binding)
		}
	}
	return bound
}

func TestANextAppBehindTheALBIsToldTheURLMapItClearsCachedPagesOn(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), behindTheALB(t, p), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}

	env := envOf(server.created[0].Template.Containers[0])
	if got, want := env["OCEL_CDN_URL_MAP"], "projects/acme-prod/global/urlMaps/ocel-alb-production-routes"; got != want {
		t.Errorf("the Next service reads OCEL_CDN_URL_MAP=%q, want %q", got, want)
	}
	if got := env["OCEL_CACHE_TAG_PURGE"]; got != "1" {
		t.Errorf("the Next service reads OCEL_CACHE_TAG_PURGE=%q, want 1: the runtime purges only with both set", got)
	}
}

func TestANextAppWithoutTheALBIsToldNoURLMap(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *Provider, *provider.StackSpec){
		"no edge": func(_ *testing.T, _ *Provider, spec *provider.StackSpec) { spec.Edge = nil },
		"an edge of none": func(t *testing.T, p *Provider, spec *provider.StackSpec) {
			front, err := p.Edges().Open(edge.None, nil)
			if err != nil {
				t.Fatal(err)
			}
			spec.Edge = front
		},
		"an edge that runs code": func(t *testing.T, p *Provider, spec *provider.StackSpec) {
			adoptBehindTheWorker(t, p, "https://writer.example.test", fake.KindRelay)
			front, err := fake.NewEdges().Open(fake.KindRelay, nil)
			if err != nil {
				t.Fatal(err)
			}
			spec.Edge = shieldingFront{front}
			spec.Ref.Name.Release = naming.NewReleaseToken("d1", "f1")
			spec.App.Guard = nil
		},
		"a node function": func(_ *testing.T, _ *Provider, spec *provider.StackSpec) {
			spec.App.Framework = "node"
			spec.App.Functions[0].Framework.Name = "node"
		},
		"a Next app without ISR": func(_ *testing.T, _ *Provider, spec *provider.StackSpec) { spec.App.ISR = nil },
	} {
		t.Run(name, func(t *testing.T) {
			server := &runServer{}
			p := server.open(t)
			spec := behindTheALB(t, p)
			change(t, p, &spec)
			if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
				t.Fatalf("ProvisionFunctions() = %v", err)
			}

			if got, told := envOf(server.created[0].Template.Containers[0])["OCEL_CDN_URL_MAP"]; told {
				t.Errorf("the service reads OCEL_CDN_URL_MAP=%q, want none", got)
			}
			if bound := purgeBindingsOf(server.identities()); len(bound) != 0 {
				t.Errorf("the app's account was granted %q, want no purge binding", bound)
			}
		})
	}
}

func TestANextAppBehindTheALBMayClearCloudCDNAndNothingElseOnTheLoadBalancer(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), behindTheALB(t, p), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	c, err := p.openClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := cdnPurgeRolePath + " serviceAccount:" + c.AppAccountEmail(environment.TierProduction, "shop", "web") + " "
	if got := purgeBindingsOf(server.identities()); len(got) != 1 || got[0] != want {
		t.Fatalf("the project's purge bindings = %q, want exactly %q: one, unconditioned, for the app's own account", got, want)
	}
	for _, binding := range server.identities().project.Bindings {
		if strings.HasPrefix(binding.Role, "roles/compute.") {
			t.Errorf("the project binds %s: the app may clear Cloud CDN through the custom role and nothing else", binding.Role)
		}
	}
}

func TestARedeployOfANextAppBehindTheALBWritesNoPolicy(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	spec := behindTheALB(t, p)
	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	before := server.identities().projectWrites

	if _, err := p.ProvisionFunctions(context.Background(), spec, nil); err != nil {
		t.Fatalf("second ProvisionFunctions() = %v", err)
	}
	if got := server.identities().projectWrites - before; got != 0 {
		t.Errorf("a redeploy wrote the project policy %d times, want 0", got)
	}
}

func TestADeployBeforeTheBootstrapKeptTheCachePurgeRoleIsRefusedNamingTheBootstrap(t *testing.T) {
	server := &runServer{}
	server.identities().missingRole = cdnPurgeRolePath
	p := server.open(t)

	_, err := p.ProvisionFunctions(context.Background(), behindTheALB(t, p), nil)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("ProvisionFunctions() = %v, want a %s refusal", err, refusal.CodeNotReady)
	}
	if !strings.Contains(err.Error(), "ocel bootstrap --features alb-edge") {
		t.Errorf("the refusal %q does not name the bootstrap that keeps the role", err)
	}
	if got := server.identities().missingRoleAsks; got != 1 {
		t.Errorf("the purge binding was tried %d times, want 1: a role that is not there is not an account that is not visible yet", got)
	}
}

func TestAnAppNoEnvironmentRunsLosesItsCachePurgeGrant(t *testing.T) {
	server := &runServer{}
	p := server.open(t)
	if _, err := p.ProvisionFunctions(context.Background(), behindTheALB(t, p), nil); err != nil {
		t.Fatal(err)
	}
	c, err := p.openClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	member := "serviceAccount:" + c.AppAccountEmail(environment.TierProduction, "shop", "web")
	if _, err := c.unbindProjectMember(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if got := purgeBindingsOf(server.identities()); len(got) != 0 {
		t.Errorf("the project's purge bindings after the app's account lost its grants = %q, want none", got)
	}
}
