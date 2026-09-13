package vps

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

var _ providerkit.ConnectorHost = (*Provider)(nil)

const connectorCompute = providerkit.ComputeContainer

func dialable(hostname string) error {
	if hostname == "" {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"the ssh destination names no host, and the console has to have an address to dial")
	}
	if net.ParseIP(hostname) != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"this machine is reached at %s, and the console dials a connector over https at a hostname the proxy on this box holds a certificate for: point a hostname at %s, name it as the ssh host, and run this again",
			hostname, hostname)
	}
	return nil
}

func hostKeyDigest(offered providerkit.HostKey) (string, error) {
	held, err := offered.Fingerprinted()
	if err != nil {
		return "", err
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(held.Key)
	if err != nil {
		return "", errors.New("the offered host key is not a base64 key blob")
	}
	sum := sha256.Sum256(blob)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (p *Provider) DescribeConnectorTarget(ctx context.Context) (providerkit.ConnectorTarget, error) {
	live, err := p.Session(ctx)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	key, err := hostKeyDigest(live.HostKey())
	if err != nil {
		return providerkit.ConnectorTarget{}, providerkit.Refuse(providerkit.CodeDenied,
			"this host's ssh key is what names the target the console keys a connector by, and %s", err)
	}
	ns, err := providerkit.NamespaceFromEnv()
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	fingerprint, err := target.Fingerprint("vps", key, ns.String())
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	hostname := live.Destination().Written
	if err := dialable(hostname); err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	arch, err := p.host.Arch(ctx)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	described := providerkit.ConnectorTarget{
		Fingerprint: fingerprint,
		Hostname:    hostname,
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
	compute, err := providerkit.ConnectorCompute(install.Compute, connectorCompute)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	live, err := p.Session(ctx)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	hostname := live.Destination().Written
	if err := dialable(hostname); err != nil {
		return providerkit.ConnectorAddress{}, err
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
