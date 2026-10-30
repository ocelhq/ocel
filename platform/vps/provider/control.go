package vps

import (
	"context"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type control struct{ *Provider }

func NewControl(_ context.Context, settings provider.Settings, onBox ssh.Conn) (provider.Provider, error) {
	decoded, err := decodeOptions(settings.Options)
	if err != nil {
		return nil, err
	}
	p := &Provider{options: decoded, project: settings.Slug, now: time.Now}
	at := addressedBox{Conn: onBox, address: decoded.SSH.Host}
	p.wire(func(context.Context) (ssh.Conn, error) { return at, nil })
	return control{p}, nil
}

type addressedBox struct {
	ssh.Conn
	address string
}

func (b addressedBox) Destination() ssh.Destination {
	dest := b.Conn.Destination()
	dest.Address = b.address
	return dest
}

func (c control) refuseUnaddressed() error {
	if c.options.SSH.Host != "" {
		return nil
	}
	return provider.RefuseNeedsRunner("reaching a box its deploys name by the ssh_config alias " + c.options.SSH.Alias)
}

func (c control) Edges() provider.Edges { return controlEdges{c} }

type controlEdges struct{ control control }

func (e controlEdges) Open(kind edge.Kind, options provider.Options) (edge.Edge, error) {
	if kind != edge.None {
		return nil, provider.RefuseNeedsRunner("opening the " + string(kind) + " edge")
	}
	if err := e.control.refuseUnaddressed(); err != nil {
		return nil, err
	}
	return e.control.Provider.Edges().Open(kind, options)
}

func (c control) Routers() provider.Routers { return controlRouters{c} }

type controlRouters struct{ control control }

func (r controlRouters) Open(kind router.Kind) (router.Router, error) {
	if err := r.control.refuseUnaddressed(); err != nil {
		return nil, err
	}
	return r.control.Provider.Routers().Open(kind)
}

func (control) DNS() provider.DNS { return absentDNS{} }

type absentDNS struct{}

func (absentDNS) Open(kind provider.DNSKind, _ string, _ edge.Kind) (edge.DNSRecords, error) {
	return nil, provider.RefuseNeedsRunner("writing " + string(kind) + " DNS records")
}

func (c control) Credentials() provider.Credentials {
	return controlCredentials{c.Provider.Credentials()}
}

type controlCredentials struct{ provider.Credentials }

func (c controlCredentials) Permissions(purpose edge.CredentialPurpose, tier environment.Tier) (edge.CredentialDocument, error) {
	if purpose == edge.PurposeBootstrap {
		return edge.CredentialDocument{}, provider.RefuseNeedsRunner("naming what the login that bootstraps the box needs")
	}
	return c.Credentials.Permissions(purpose, tier)
}

func (controlCredentials) Whoami(context.Context) (provider.Principal, error) {
	return provider.Principal{}, provider.RefuseNeedsRunner("naming who deploys over ssh")
}

func (c control) Hooks() provider.Hooks {
	hooks := c.Provider.Hooks()
	hooks.Preflight = &provider.PreflightHooks{
		Plan: func(context.Context, provider.DeployPreflight) (provider.Plan, error) {
			return provider.Plan{}, provider.RefuseNeedsRunner("a deploy's preflight")
		},
		Apply: func(context.Context, provider.DeployPreflight) error {
			return provider.RefuseNeedsRunner("a deploy's preflight")
		},
	}
	hooks.OpenRegistryImages = func(context.Context, provider.RegistryTarget) (provider.ImageStore, error) {
		return nil, provider.RefuseNeedsRunner("sending images")
	}
	hooks.OpenDirectImages = func(context.Context) (provider.ImageStore, error) {
		return nil, provider.RefuseNeedsRunner("sending images")
	}
	hooks.CheckHost = func(context.Context, provider.HostCheckRequest) ([]provider.HostCheck, error) {
		return nil, provider.RefuseNeedsRunner("checking the box")
	}
	hooks.ListProjectResources = func(ctx context.Context, req provider.ProjectResourcesRequest) ([]provider.PhysicalResource, error) {
		if req.EdgeKind != edge.None || req.DNSKind != "" {
			return nil, provider.RefuseNeedsRunner("listing what the " + string(req.EdgeKind) + " edge holds")
		}
		return nil, nil
	}
	hooks.BindingAccess = &provider.BindingAccessHooks{
		ForwardPorts: func(context.Context, provider.PortForwardRequest, progress.Log) ([]provider.PortForward, error) {
			return nil, provider.RefuseNeedsRunner("forwarding ports")
		},
		ServeProxy: func(context.Context, provider.BindingProxyRequest, progress.Log) (provider.BindingProxy, error) {
			return provider.BindingProxy{}, provider.RefuseNeedsRunner("serving a binding proxy")
		},
	}
	return hooks
}

func (control) Bootstrap(edge.Kind) (provider.Bootstrap, error) {
	return nil, provider.RefuseNeedsRunner("a bootstrap")
}

func (control) Stacks() provider.Stacks { return provider.AbsentEngine{} }

func (control) Runtime() provider.Runtime { return provider.AbsentEngine{} }

func (control) Connector() provider.Connector { return absentConnector{} }

type absentConnector struct{}

func (absentConnector) Target(context.Context) (provider.ConnectorTarget, error) {
	return provider.ConnectorTarget{}, provider.RefuseNeedsRunner("describing the connector's target")
}

func (absentConnector) Install(context.Context, provider.ConnectorInstall, progress.Log) (provider.ConnectorAddress, error) {
	return provider.ConnectorAddress{}, provider.RefuseNeedsRunner("installing a connector")
}

func (absentConnector) Remove(context.Context, progress.Log) error {
	return provider.RefuseNeedsRunner("removing a connector")
}
