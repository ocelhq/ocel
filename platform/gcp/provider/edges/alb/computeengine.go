package alb

import (
	"context"
	"fmt"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func NewComputeEngine(openClients func(context.Context) (*gcloud.Clients, error)) Engine {
	return Engine{
		Stacks:        ComputeStacks{OpenClients: openClients},
		Images:        refusingImages{},
		FrontPart:     FrontPart{Claim: refuseFrontPart, Release: refuseFrontPart},
		EvaluateFront: refuseEvaluateFront,
		ReadApps:      refuseReadApps,
	}
}

type ComputeStacks struct {
	OpenClients func(context.Context) (*gcloud.Clients, error)
}

func (s ComputeStacks) UpLoadBalancer(_ context.Context, spec albresources.LoadBalancerSpec, _ progress.Log) (map[string]string, error) {
	return nil, refuseRaise(albresources.Stack{Tier: spec.Tier})
}

func (s ComputeStacks) UpBinding(_ context.Context, spec albresources.BindingSpec, _ progress.Log) (map[string]string, error) {
	return nil, refuseRaise(albresources.Stack{Tier: spec.Tier, Slug: spec.Slug})
}

func (s ComputeStacks) UpGate(_ context.Context, tier environment.Tier, _ func(context.Context, bool) (albresources.GateSpec, error), _ progress.Log) (map[string]string, error) {
	return nil, refuseRaise(albresources.Stack{Tier: tier, Gate: true})
}

func refuseRaise(stack albresources.Stack) error {
	return provider.RefuseNeedsRunner("provisioning the " + stack.Name() + " stack of the alb edge")
}

func (s ComputeStacks) Destroy(_ context.Context, stack albresources.Stack, _ progress.Log) error {
	return provider.RefuseNeedsRunner("destroying the " + stack.Name() + " stack of the alb edge")
}

func (s ComputeStacks) Outputs(ctx context.Context, stack albresources.Stack) (map[string]string, error) {
	if stack.Gate || stack.Slug != "" {
		return nil, provider.RefuseNeedsRunner("reading the " + stack.Name() + " stack of the alb edge")
	}
	clients, err := s.OpenClients(ctx)
	if err != nil {
		return nil, err
	}
	engine, err := clients.Compute()
	if err != nil {
		return nil, err
	}
	names := albresources.LoadBalancerNames(string(clients.Namespace()), stack.Tier)
	address, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*compute.Address, error) {
		return engine.GlobalAddresses.Get(clients.Project(), names.Address).Context(ctx).Do(call...)
	})
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the address of the %s load balancer of tier %s: %w", albresources.Kind, stack.Tier, err)
	}
	return map[string]string{
		albresources.OutputAddress:        address.Address,
		albresources.OutputCertificateMap: names.CertificateMap,
		albresources.OutputURLMap:         names.URLMap,
		albresources.OutputNotFound:       names.NotFound,
		albresources.OutputAssetsBackend:  names.AssetsBackend,
	}, nil
}

type refusingImages struct{}

func (refusingImages) Ensure(context.Context, environment.Tier, []byte) (string, error) {
	return "", provider.RefuseNeedsRunner("pushing the alb edge's gate plugin image")
}

func (refusingImages) Delete(context.Context, environment.Tier) error {
	return provider.RefuseNeedsRunner("deleting the alb edge's gate plugin image")
}

func refuseFrontPart(context.Context, environment.Tier, string) error {
	return provider.RefuseNeedsRunner("changing who holds the alb edge's load balancer")
}

func refuseEvaluateFront(context.Context, environment.Tier) (transform.Result, error) {
	return transform.Result{}, provider.RefuseNeedsRunner("evaluating the transforms that patch the alb edge's load balancer")
}

func refuseReadApps(context.Context, environment.Tier, string) (map[string]transform.Result, error) {
	return nil, provider.RefuseNeedsRunner("evaluating the transforms that patch the alb edge's app resources")
}
