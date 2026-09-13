package vps

import (
	"context"
	"net"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

var _ providerkit.ConnectorHost = (*Provider)(nil)

const connectorCompute = providerkit.ComputeContainer

func connectorComputeOf(asked providerkit.Compute) (providerkit.Compute, error) {
	if asked == "" || asked == connectorCompute {
		return connectorCompute, nil
	}
	return "", providerkit.Refuse(providerkit.CodeInvalid,
		"a box runs the connector as a standing process under systemd, which is %s; %s is a compute no machine hands out",
		connectorCompute, asked)
}

func (p *Provider) DescribeConnectorTarget(ctx context.Context) (providerkit.ConnectorTarget, error) {
	live, err := p.Session(ctx)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	key, err := live.HostKey().Fingerprinted()
	if err != nil {
		return providerkit.ConnectorTarget{}, providerkit.Refuse(providerkit.CodeDenied,
			"this host's ssh key is what names the target the console keys a connector by, and %s", err)
	}
	ns, err := providerkit.NamespaceFromEnv()
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	fingerprint, err := target.ForVPS(key.Fingerprint, ns.String())
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	arch, err := p.host.Arch(ctx)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	described := providerkit.ConnectorTarget{
		Fingerprint: fingerprint,
		Hostname:    live.Destination().Written,
		Arch:        arch,
	}
	standing, err := host.NewConnector(p.host).Describe(ctx)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	if standing.Installed {
		described.Installed = &providerkit.ConnectorRelease{
			Version:   standing.Version,
			PublicKey: standing.PublicKey,
			Compute:   connectorCompute,
		}
	}
	return described, nil
}

func (p *Provider) InstallConnector(ctx context.Context, install providerkit.ConnectorInstall, report providerkit.Reporter) (providerkit.ConnectorAddress, error) {
	compute, err := connectorComputeOf(install.Compute)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	live, err := p.Session(ctx)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	hostname := live.Destination().Written
	if net.ParseIP(hostname) != nil {
		return providerkit.ConnectorAddress{}, providerkit.Refuse(providerkit.CodeNotReady,
			"this machine is reached at %s, and the console dials a connector over https at a hostname the proxy on this box holds a certificate for: point a hostname at %s, name it as the ssh host, and run this again",
			hostname, hostname)
	}
	if hostname == "" {
		return providerkit.ConnectorAddress{}, providerkit.Refuse(providerkit.CodeNotReady,
			"the ssh destination names no host, and the console has to have an address to dial")
	}
	if report != nil {
		report.Say("connector " + install.Version + " onto " + hostname)
	}
	standing, err := host.NewConnector(p.host).Install(ctx, hostname, install.Binary, install.Config, report)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	return providerkit.ConnectorAddress{
		URL:       "https://" + hostname + host.ConnectorPath,
		PublicKey: standing.PublicKey,
		Compute:   compute,
	}, nil
}

func (p *Provider) RemoveConnector(ctx context.Context, report providerkit.Reporter) error {
	if _, err := p.Session(ctx); err != nil {
		return err
	}
	return host.NewConnector(p.host).Remove(ctx, report)
}
