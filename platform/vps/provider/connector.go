package vps

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/target"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const connectorCompute = provider.ComputeContainer

func dialable(hostname string) error {
	if hostname == "" {
		return refusal.Refuse(refusal.CodeNotReady,
			"the ssh destination names no host")
	}
	if net.ParseIP(hostname) != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"the ssh host is the address %s; the connector needs a hostname\nPoint a hostname at %s and name it as the ssh host",
			hostname, hostname)
	}
	return nil
}

func hostKeyDigest(offered provider.HostKey) (string, error) {
	fingerprinted, err := offered.Fingerprinted()
	if err != nil {
		return "", err
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fingerprinted.Key)
	if err != nil {
		return "", errors.New("the offered host key is not a base64 key blob")
	}
	sum := sha256.Sum256(blob)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

type connector struct{ *Provider }

func (p connector) Target(ctx context.Context) (provider.ConnectorTarget, error) {
	live, err := p.Session(ctx)
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	key, err := hostKeyDigest(live.HostKey())
	if err != nil {
		return provider.ConnectorTarget{}, refusal.Refuse(refusal.CodeDenied,
			"read this host's ssh key: %s", err)
	}
	ns, err := provider.NamespaceFromEnv()
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	fingerprint, err := target.Fingerprint("vps", key, ns.String())
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	hostname := live.Destination().Written
	if err := dialable(hostname); err != nil {
		return provider.ConnectorTarget{}, err
	}
	arch, err := p.host.Arch(ctx)
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	described := provider.ConnectorTarget{
		Fingerprint: fingerprint,
		Hostname:    hostname,
		Arch:        arch,
	}
	current, err := host.NewConnector(p.host).Describe(ctx)
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	if current.Installed {
		described.Installed = &provider.ConnectorRelease{
			Version:   current.Version,
			PublicKey: current.PublicKey,
			Compute:   connectorCompute,
		}
	}
	return described, nil
}

func (p connector) Install(ctx context.Context, install provider.ConnectorInstall, progress edge.Progress) (provider.ConnectorAddress, error) {
	compute, err := provider.ConnectorCompute(install.Compute, connectorCompute)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	live, err := p.Session(ctx)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	hostname := live.Destination().Written
	if err := dialable(hostname); err != nil {
		return provider.ConnectorAddress{}, err
	}
	if progress != nil {
		progress.Say("connector " + install.Version + " onto " + hostname)
	}
	installed, err := host.NewConnector(p.host).Install(ctx, hostname, install.Binary, install.Config, progress)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	return provider.ConnectorAddress{
		URL:       "https://" + hostname + switchboard.ConnectorPath,
		PublicKey: installed.PublicKey,
		Compute:   compute,
	}, nil
}

func (p connector) Remove(ctx context.Context, progress edge.Progress) error {
	if _, err := p.Session(ctx); err != nil {
		return err
	}
	return host.NewConnector(p.host).Remove(ctx, progress)
}
