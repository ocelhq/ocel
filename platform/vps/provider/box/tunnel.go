package box

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type Tunnels func(kind edge.Kind) (*edge.TunnelHooks, error)

func (e *Edge) openTunnel(ctx context.Context, kind edge.Kind) (host.Tunnel, error) {
	hooks, err := e.tunnels(kind)
	if err != nil {
		return host.Tunnel{}, err
	}
	reserved, err := e.machine.ReserveTunnel(ctx, kind)
	if err != nil {
		return host.Tunnel{}, err
	}
	opened, err := hooks.Ensure(ctx, reserved.Name)
	if err != nil {
		return host.Tunnel{}, err
	}
	if opened.ID != reserved.ID {
		if err := hooks.Configure(ctx, opened.ID, host.TunnelService); err != nil {
			return host.Tunnel{}, err
		}
	}
	reserved.ID, reserved.Address = opened.ID, opened.Address
	token := func(ctx context.Context) (string, error) { return hooks.ReadToken(ctx, opened.ID) }
	if err := e.machine.RunTunnel(ctx, reserved, token); err != nil {
		if errors.Is(err, host.ErrTunnelReleased) {
			return host.Tunnel{}, errors.Join(err, hooks.Delete(ctx, opened.ID))
		}
		return host.Tunnel{}, err
	}
	return reserved, nil
}

func (e *Edge) tunnelHost(ctx context.Context, claim string, kind edge.Kind, owner string) (edge.Origin, error) {
	tunnel, err := e.openTunnel(ctx, kind)
	if err != nil {
		return edge.Origin{}, err
	}
	if err := e.machine.TunnelHost(ctx, host.TunneledHost{Hostname: claim, Owner: owner}, tunnel.Name); err != nil {
		return edge.Origin{}, err
	}
	if err := e.machine.RemoveShield(ctx, claim, owner); err != nil {
		return edge.Origin{}, err
	}
	return edge.Origin{Address: tunnel.Address, Certified: true, Tunneled: true}, nil
}

func (e *Edge) removeTunneledHost(ctx context.Context, hostname, owner string) error {
	if err := e.machine.RemoveTunneledHost(ctx, host.TunneledHost{Hostname: hostname, Owner: owner}); err != nil {
		return err
	}
	return e.releaseTunnel(ctx)
}

func (e *Edge) releaseTunnel(ctx context.Context) error {
	retired, err := e.machine.ReleaseTunnel(ctx)
	errs := []error{err}
	for _, tunnel := range retired {
		errs = append(errs, e.deleteTunnel(ctx, tunnel))
	}
	return errors.Join(errs...)
}

func (e *Edge) deleteTunnel(ctx context.Context, tunnel host.Tunnel) error {
	hooks, err := e.tunnels(tunnel.Edge)
	if err == nil {
		err = deleteAtEdge(ctx, hooks, tunnel)
	}
	if err != nil {
		return fmt.Errorf("nothing on this box goes through the tunnel %s any more, and it is stopped here, but deleting it at the %s edge failed, so the next release deletes it: %w", cmp.Or(tunnel.ID, tunnel.Name), tunnel.Edge, err)
	}
	return e.machine.ForgetTunnel(ctx, tunnel.Name)
}

func deleteAtEdge(ctx context.Context, hooks *edge.TunnelHooks, tunnel host.Tunnel) error {
	if tunnel.ID != "" {
		return hooks.Delete(ctx, tunnel.ID)
	}
	opened, err := hooks.Ensure(ctx, tunnel.Name)
	if err != nil {
		return err
	}
	return hooks.Delete(ctx, opened.ID)
}
