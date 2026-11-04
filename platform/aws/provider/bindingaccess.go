package aws

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres/signin"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/valkey"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest, progress progress.Log) ([]provider.PortForward, error) {
	if len(req.Bindings) == 0 {
		return nil, nil
	}
	deployed, err := p.ReadDeployed(ctx, req.Tier)
	if err != nil {
		return nil, err
	}
	if err := deployed.RefuseUnbootstrapped(req.Tier); err != nil {
		return nil, err
	}
	resolved, err := readForwardedPasswords(ctx, signin.SourcesFor(p.Config), req.Bindings)
	if err != nil {
		return nil, err
	}
	clients := bastion.NewClients(p.Config)
	reconciled, err := bastionPlacement{
		claim: func(ctx context.Context) (network.Network, error) {
			return p.stacks.Network().ClaimWithoutRepatch(ctx, bastion.ConsumerRef(req.Tier), progress)
		},
		reconcile: func(ctx context.Context, placed network.Network) (bastion.Bastion, error) {
			return bastion.Reconcile(ctx, clients, bastion.Spec{
				Namespace: string(p.Namespace),
				Tier:      req.Tier,
				Network:   bastion.Network{VPC: placed.VPC, CIDR: placed.CIDR, Subnets: placed.Public, Clients: placed.Clients},
				Boundary:  deployed.AppBoundaryARN,
				Ports:     []int{postgres.Port, valkey.Port},
			})
		},
		isBuilt: func(ctx context.Context) (bool, error) {
			_, found, err := bastion.Describe(ctx, clients, string(p.Namespace), req.Tier)
			return found, err
		},
		remove: func(ctx context.Context) error {
			return bastion.Remove(ctx, clients, string(p.Namespace), req.Tier)
		},
		release: func(ctx context.Context) error {
			return p.releaseBastionNetwork(ctx, req.Tier, progress)
		},
	}.place(ctx)
	if err != nil {
		return nil, err
	}
	forwards, err := reconciled.Forward(ctx, clients, bastion.OpenSession(p.Config), req.ReportFailure, req.Bindings)
	if err != nil {
		return nil, err
	}
	for at := range forwards {
		forwards[at].Properties = resolved[forwards[at].Binding]
	}
	return forwards, nil
}

type bastionPlacement struct {
	claim     func(context.Context) (network.Network, error)
	reconcile func(context.Context, network.Network) (bastion.Bastion, error)
	isBuilt   func(context.Context) (bool, error)
	remove    func(context.Context) error
	release   func(context.Context) error
}

func (b bastionPlacement) place(ctx context.Context) (bastion.Bastion, error) {
	placed, err := b.claim(ctx)
	if err != nil {
		return bastion.Bastion{}, err
	}
	reconciled, err := b.reconcile(ctx, placed)
	if err == nil {
		return reconciled, nil
	}
	built, readErr := b.isBuilt(ctx)
	if readErr != nil || built {
		return bastion.Bastion{}, errors.Join(err, readErr)
	}
	if removeErr := b.remove(ctx); removeErr != nil {
		return bastion.Bastion{}, errors.Join(err, removeErr)
	}
	return bastion.Bastion{}, errors.Join(err, b.release(ctx))
}

func readForwardedPasswords(ctx context.Context, sources signin.Sources, bindings []provider.Binding) (map[string]map[string]string, error) {
	resolved := map[string]map[string]string{}
	for _, binding := range bindings {
		if binding.Properties[provider.PropertyPassword] != "" {
			continue
		}
		password, signsIn, err := sources.Read(ctx, binding)
		if err != nil {
			return nil, err
		}
		if signsIn {
			resolved[binding.Name] = map[string]string{provider.PropertyPassword: password.Value}
		}
	}
	return resolved, nil
}
