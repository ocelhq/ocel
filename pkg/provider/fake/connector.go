package fake

import (
	"context"
	"errors"
	"slices"
	"sync"

	"connectrpc.com/connect"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const ConnectorPublicKey = "ZmFrZS1jb25uZWN0b3Ita2V5"

var errNoConnector = connect.NewError(connect.CodeUnimplemented,
	errors.New("this provider puts no connector on its targets; the console reaches a target of this kind through the provider itself"))

type Connector struct {
	mu        sync.Mutex
	target    *provider.ConnectorTarget
	refusal   error
	computes  []provider.Compute
	installed []provider.ConnectorInstall
	removals  int
}

func (c *Connector) Runs(target provider.ConnectorTarget) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.target = &target
}

func (c *Connector) RunsOn(computes ...provider.Compute) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.computes = computes
}

func (c *Connector) handedOut() []provider.Compute {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.computes == nil {
		return []provider.Compute{provider.ComputeContainer}
	}
	return slices.Clone(c.computes)
}

func (c *Connector) Refuse(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refusal = err
}

func (c *Connector) Installed() []provider.ConnectorInstall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.installed)
}

func (c *Connector) Removals() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removals
}

func (c *Connector) reachable() (provider.ConnectorTarget, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refusal != nil {
		return provider.ConnectorTarget{}, c.refusal
	}
	if c.target == nil {
		return provider.ConnectorTarget{}, errNoConnector
	}
	return *c.target, nil
}

func (c *Connector) Target(context.Context) (provider.ConnectorTarget, error) {
	return c.reachable()
}

func (c *Connector) Install(_ context.Context, install provider.ConnectorInstall, progress progress.Log) (provider.ConnectorAddress, error) {
	target, err := c.reachable()
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	compute, err := provider.ConnectorCompute(install.Compute, c.handedOut()...)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	c.mu.Lock()
	c.installed = append(c.installed, install)
	c.mu.Unlock()
	if progress != nil {
		progress.Say("wrote the connector")
	}
	return provider.ConnectorAddress{
		URL:       "https://" + target.Hostname + "/" + statedir.Name + "/connector",
		PublicKey: ConnectorPublicKey,
		Compute:   compute,
	}, nil
}

func (c *Connector) Remove(context.Context, progress.Log) error {
	if _, err := c.reachable(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removals++
	return nil
}
