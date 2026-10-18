package switchboard

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

type Tunnels func(kind edge.Kind) (*edge.TunnelHooks, error)

func (e *Edge) openTunnel(ctx context.Context, kind edge.Kind) (boxapi.Tunnel, error) {
	hooks, err := e.tunnels(kind)
	if err != nil {
		return boxapi.Tunnel{}, err
	}
	reserved, err := e.tunnel.Reserve(ctx, kind, hooks.Connector)
	if err != nil {
		return boxapi.Tunnel{}, err
	}
	opened, err := hooks.Ensure(ctx, reserved.Name)
	if err != nil {
		return boxapi.Tunnel{}, err
	}
	if opened.ID != reserved.ID {
		if err := hooks.Configure(ctx, opened.ID, tunnel.Upstream); err != nil {
			return boxapi.Tunnel{}, err
		}
	}
	reserved.ID, reserved.Address = opened.ID, opened.Address
	token := func(ctx context.Context) (string, error) { return hooks.ReadToken(ctx, opened.ID) }
	if err := e.tunnel.Run(ctx, reserved, hooks.Connector, token); err != nil {
		if errors.Is(err, routing.ErrTunnelReleased) {
			return boxapi.Tunnel{}, errors.Join(err, hooks.Delete(ctx, opened.ID))
		}
		return boxapi.Tunnel{}, err
	}
	return reserved, nil
}

func (e *Edge) claimThrough(ctx context.Context, kind edge.Kind, owner, hostname string, claim func(tunnel boxapi.Tunnel) error) (edge.Origin, error) {
	tunnel, err := e.openTunnel(ctx, kind)
	if err != nil {
		return edge.Origin{}, err
	}
	if err := claim(tunnel); err != nil {
		if errors.Is(err, routing.ErrTunnelReleased) {
			return edge.Origin{}, errors.Join(err, e.tunnel.Stop(ctx, tunnel))
		}
		return edge.Origin{}, err
	}
	if err := e.routes.RemoveShield(ctx, hostname, owner); err != nil {
		return edge.Origin{}, err
	}
	return edge.Origin{Address: tunnel.Address, Certified: true, Tunneled: true}, nil
}

func (e *Edge) releaseTunnel(ctx context.Context) error {
	retired, err := e.tunnel.Release(ctx)
	errs := []error{err}
	for _, tunnel := range retired {
		errs = append(errs, e.deleteTunnel(ctx, tunnel))
	}
	return errors.Join(errs...)
}

func (e *Edge) deleteTunnel(ctx context.Context, tunnel boxapi.Tunnel) error {
	hooks, err := e.tunnels(tunnel.Edge)
	if err == nil {
		err = deleteAtEdge(ctx, hooks, tunnel)
	}
	if err != nil {
		return fmt.Errorf("nothing on this box goes through the tunnel %s any more, and it is stopped here, but deleting it at the %s edge failed, so the next release deletes it: %w", cmp.Or(tunnel.ID, tunnel.Name), tunnel.Edge, err)
	}
	return e.tunnel.Forget(ctx, tunnel.Name)
}

func deleteAtEdge(ctx context.Context, hooks *edge.TunnelHooks, tunnel boxapi.Tunnel) error {
	if tunnel.ID != "" {
		return hooks.Delete(ctx, tunnel.ID)
	}
	opened, err := hooks.Ensure(ctx, tunnel.Name)
	if err != nil {
		return err
	}
	return hooks.Delete(ctx, opened.ID)
}
