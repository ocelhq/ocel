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
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const connectorCompute = providerkit.ComputeContainer

func dialable(hostname string) error {
	if hostname == "" {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"the ssh destination names no host")
	}
	if net.ParseIP(hostname) != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"the ssh host is the address %s; the connector needs a hostname\nPoint a hostname at %s and name it as the ssh host",
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

type connector struct{ *Provider }

func (p connector) Target(ctx context.Context) (providerkit.ConnectorTarget, error) {
	live, err := p.Session(ctx)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	key, err := hostKeyDigest(live.HostKey())
	if err != nil {
		return providerkit.ConnectorTarget{}, providerkit.Refuse(providerkit.CodeDenied,
			"read this host's ssh key: %s", err)
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

func (p connector) Install(ctx context.Context, install providerkit.ConnectorInstall, progress providerkit.Progress) (providerkit.ConnectorAddress, error) {
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
	if progress != nil {
		progress.Say("connector " + install.Version + " onto " + hostname)
	}
	standing, err := host.NewConnector(p.host).Install(ctx, hostname, install.Binary, install.Config, progress)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	return providerkit.ConnectorAddress{
		URL:       "https://" + hostname + switchboard.ConnectorPath,
		PublicKey: standing.PublicKey,
		Compute:   compute,
	}, nil
}

func (p connector) Remove(ctx context.Context, progress providerkit.Progress) error {
	if _, err := p.Session(ctx); err != nil {
		return err
	}
	return host.NewConnector(p.host).Remove(ctx, progress)
}
