package aws

import (
	"context"
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
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest, _ progress.Log) ([]provider.PortForward, error) {
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
	vpcClients, err := p.vpcClients(ctx, req.Tier)
	if err != nil {
		return nil, err
	}
	reconciled, err := bastion.Reconcile(ctx, clients, bastion.Spec{
		Tier:     req.Tier,
		Clients:  vpcClients,
		Boundary: deployed.AppBoundaryARN,
		Ports:    []int{postgres.Port, valkey.Port},
	})
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
