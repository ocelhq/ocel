package gcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func TestAContainerWhoseAppIsBilledPerRequestIdlesItsCPUBetweenRequests(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	p.options.Apps = map[string]AppOptions{"web": {Billing: run.BillingRequest}}

	if _, err := p.ProvisionContainers(context.Background(), productionSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if !server.Created[0].Template.Containers[0].Resources.CpuIdle {
		t.Error("a container whose app sets billing request keeps its CPU between requests, want it idle: the option overrides a container's instance billing")
	}
}

func TestANextFunctionWhoseAppIsBilledPerRequestIdlesItsCPUBetweenRequests(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	p.options.Apps = map[string]AppOptions{"web": {Billing: run.BillingRequest}}

	if _, err := p.ProvisionFunctions(context.Background(), nextSpec(), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if !server.Created[0].Template.Containers[0].Resources.CpuIdle {
		t.Error("a Next function whose app sets billing request keeps its CPU between requests, want it idle: the option wins over the Next default")
	}
}

func TestANodeFunctionWhoseAppIsBilledPerInstanceKeepsItsCPUBetweenRequests(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	p.options.Apps = map[string]AppOptions{"web": {Billing: run.BillingInstance}}

	if _, err := p.ProvisionFunctions(context.Background(), functionSpec(productionSpec(), "nodejs"), nil); err != nil {
		t.Fatalf("ProvisionFunctions() = %v", err)
	}
	if server.Created[0].Template.Containers[0].Resources.CpuIdle {
		t.Error("a node function whose app sets billing instance idles its CPU between requests, want it kept")
	}
}

func TestADeployWhoseOptionsSetBillingForNoAppOfTheProjectIsRefusedBeforeAnythingIsRead(t *testing.T) {
	ctx := context.Background()
	for name, preflight := range map[string]func(*Provider, provider.DeployPreflight) error{
		"plan": func(p *Provider, pre provider.DeployPreflight) error {
			_, err := p.PlanPreflight(ctx, pre)
			return err
		},
		"apply": func(p *Provider, pre provider.DeployPreflight) error { return p.ApplyPreflight(ctx, pre) },
	} {
		t.Run(name, func(t *testing.T) {
			p := failingOnAnyCall(t)
			p.options.Apps = map[string]AppOptions{"wbe": {Billing: run.BillingRequest}}

			var refused refusal.Refusal
			err := preflight(p, shippingNext(false))
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("%s = %v, want an invalid refusal: a misspelt app would otherwise fall back to its default billing unseen", name, err)
			}
			if !strings.Contains(refused.Message, `"apps.wbe"`) || !strings.Contains(refused.Message, "web") {
				t.Errorf("%s refused with %q, want it to name the option apps.wbe and the app web the project has", name, refused.Message)
			}
		})
	}
}

func TestADeployWhoseOptionsSetBillingForItsOwnAppIsAdmitted(t *testing.T) {
	p := pushing(t, servingOnlyEnabledAPIs(t))
	recordedCurrent(t, p, environment.TierProduction, bootstrap.Keys, bootstrap.EdgeBucket, bootstrap.CacheBucket)
	p.options.Apps = map[string]AppOptions{"web": {Billing: run.BillingRequest}}

	if err := p.ApplyPreflight(context.Background(), shippingNext(false)); err != nil {
		t.Errorf("ApplyPreflight() = %v, want a deploy setting billing for its app web admitted", err)
	}
}

func TestAnAppsBillingLeavesAnotherAppsServicesOnTheirDefault(t *testing.T) {
	server := &googletest.Run{}
	p := providerServing(server, t)
	p.options.Apps = map[string]AppOptions{"jobs": {Billing: run.BillingRequest}}

	if _, err := p.ProvisionContainers(context.Background(), productionSpec(), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if server.Created[0].Template.Containers[0].Resources.CpuIdle {
		t.Error("the web container idles its CPU because jobs sets billing request, want web kept on a container's instance billing")
	}
}
