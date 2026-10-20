package aws

import (
	"context"
	"errors"
	"slices"

	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres/signin"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/buildproxy"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/pkg/valkey"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket/dataplane"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest, progress progress.Log) ([]provider.PortForward, error) {
	if len(req.Bindings) == 0 {
		return nil, nil
	}
	deployed, err := p.bootstrapped(ctx, req.Tier)
	if err != nil {
		return nil, err
	}
	if err := p.requireBootstrapped(deployed, req.Tier); err != nil {
		return nil, err
	}
	resolved, err := readForwardedPasswords(ctx, signin.SourcesFor(p.aws), req.Bindings)
	if err != nil {
		return nil, err
	}
	clients := bastion.NewClients(p.aws)
	reconciled, err := bastionPlacement{
		claim: func(ctx context.Context) (network.Network, error) {
			return p.stacks.Network().Claim(ctx, bastion.ConsumerRef(req.Tier), progress)
		},
		reconcile: func(ctx context.Context, placed network.Network) (bastion.Bastion, error) {
			return bastion.Reconcile(ctx, clients, bastion.Spec{
				Namespace: string(p.namespace),
				Tier:      req.Tier,
				Network:   bastion.Network{VPC: placed.VPC, CIDR: placed.CIDR, Subnets: placed.Public, Clients: placed.Clients},
				Boundary:  deployed.AppBoundaryARN,
				Ports:     []int{postgres.Port, valkey.Port},
			})
		},
		isBuilt: func(ctx context.Context) (bool, error) {
			_, found, err := bastion.Describe(ctx, clients, string(p.namespace), req.Tier)
			return found, err
		},
		remove: func(ctx context.Context) error {
			return bastion.Remove(ctx, clients, string(p.namespace), req.Tier)
		},
		release: func(ctx context.Context) error {
			return p.releaseBastionNetwork(ctx, req.Tier, progress)
		},
	}.place(ctx)
	if err != nil {
		return nil, err
	}
	forwards, err := reconciled.Forward(ctx, clients, bastion.OpenSession(p.aws), req.ReportFailure, req.Bindings)
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

func (p *Provider) ServeBindingProxy(ctx context.Context, req provider.BindingProxyRequest, _ progress.Log) (provider.BindingProxy, error) {
	return buildproxy.Serve(ctx, req, []provider.BindingType{provider.BindingBucket}, func(ctx context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error) {
		deployed, err := p.bootstrapped(ctx, req.Tier)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		if err := p.requireBootstrapped(deployed, req.Tier); err != nil {
			return provider.BindingProxy{}, err
		}
		objects := s3.NewFromConfig(p.aws)
		table := dynamodb.NewFromConfig(p.aws)
		presigner := s3.NewPresignClient(objects)
		served := make([]bindingproxy.Grant, 0, len(grants))
		for _, grant := range grants {
			buckets := buildproxy.ListBucketNames(grant)
			served = append(served, bindingproxy.Grant{Grantee: grant.Grantee, Services: bindingproxy.Services{Buckets: dataplane.New(dataplane.Config{
				DDB:              table,
				Presigner:        presigner,
				Objects:          objects,
				Table:            deployed.StateTable,
				SessionKeyPrefix: naming.SessionKeyPrefix(req.Slug, req.Env),
				Granted:          func() []string { return slices.Clone(buckets) },
			})}})
		}
		proxy, err := bindingproxy.Serve(served, req.ReportFailure)
		if err != nil {
			return provider.BindingProxy{}, err
		}
		sessions := make([]provider.BindingSession, 0, len(proxy.Sessions))
		for _, session := range proxy.Sessions {
			sessions = append(sessions, provider.BindingSession{Grantee: session.Grantee, SessionToken: session.Token})
		}
		return provider.BindingProxy{Address: proxy.Address, Sessions: sessions, Close: func() { _ = proxy.Close() }}, nil
	})
}
