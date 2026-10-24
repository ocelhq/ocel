package routing

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"

	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
)

func (t *Table) Claims(ctx context.Context) ([]boxapi.Claim, error) {
	state, err := t.Read(ctx)
	if err != nil {
		return nil, err
	}
	return state.Claims, nil
}

func (t *Table) Serving(ctx context.Context, key boxapi.RouteKey) (string, error) {
	state, err := t.Read(ctx)
	if err != nil {
		return "", err
	}
	at := slices.IndexFunc(state.Routes, func(route boxapi.Route) bool { return route.RouteKey == key })
	if at < 0 {
		return "", nil
	}
	return state.Routes[at].Upstream, nil
}

func (t *Table) RouteResource(ctx context.Context, route boxapi.Route) error {
	if err := RefuseInvalid(boxapi.RoutingTable{Routes: []boxapi.Route{route}}); err != nil {
		return err
	}
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Routes = Route(state.Routes, route)
		return state, nil
	})
}

func (t *Table) UnrouteApp(ctx context.Context, key boxapi.RouteKey) error {
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Routes = Unroute(state.Routes, func(route boxapi.Route) bool { return route.RouteKey == key })
		state.Claims = Disclaim(state.Claims, func(claim boxapi.Claim) bool {
			return claim.Owner == key.Owner && claim.Pointer == key.Pointer && claim.App == key.App
		})
		return state, nil
	})
}

func (t *Table) UnroutePointer(ctx context.Context, owner, pointer string) error {
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Routes = Unroute(state.Routes, func(route boxapi.Route) bool {
			return route.Owner == owner && route.Pointer == pointer
		})
		return state, nil
	})
}

func (t *Table) UnrouteSurface(ctx context.Context, owner string) error {
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Routes = Unroute(state.Routes, func(route boxapi.Route) bool { return route.Owner == owner })
		return state, nil
	})
}

func (t *Table) ClaimHosts(ctx context.Context, claims []boxapi.Claim) error {
	if err := RefuseInvalid(boxapi.RoutingTable{Claims: claims}); err != nil {
		return err
	}
	if len(claims) == 0 {
		return nil
	}
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		for _, claim := range claims {
			taken, err := Claim(state.Claims, claim)
			if err != nil {
				return boxapi.RoutingTable{}, err
			}
			state.Claims = taken
		}
		return state, nil
	})
}

func (t *Table) DisclaimHost(ctx context.Context, hostname, owner string) error {
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Claims = Disclaim(state.Claims, func(claim boxapi.Claim) bool {
			return claim.Hostname == hostname && claim.Owner == owner
		})
		return state, nil
	})
}

func (t *Table) DisclaimPointer(ctx context.Context, owner, pointer string) error {
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Claims = Disclaim(state.Claims, func(claim boxapi.Claim) bool {
			return claim.Owner == owner && claim.Pointer == pointer
		})
		return state, nil
	})
}

func (t *Table) DisclaimSurface(ctx context.Context, owner string) error {
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Claims = Disclaim(state.Claims, func(claim boxapi.Claim) bool { return claim.Owner == owner })
		state.Shields = DropShields(state.Shields, func(shield boxapi.Shield) bool { return shield.Owner == owner })
		state.Gates = dropGates(state.Gates, owner)
		return state, nil
	})
}

func (t *Table) PutGate(ctx context.Context, owner string, held *gate.Gate) error {
	if held != nil {
		if err := RefuseInvalid(boxapi.RoutingTable{Gates: []boxapi.OwnedGate{{Owner: owner, Gate: *held}}}); err != nil {
			return err
		}
	}
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Gates = dropGates(state.Gates, owner)
		if held != nil {
			state.Gates = append(state.Gates, boxapi.OwnedGate{Owner: owner, Gate: *held})
		}
		return state, nil
	})
}

func dropGates(gates []boxapi.OwnedGate, owner string) []boxapi.OwnedGate {
	return slices.DeleteFunc(slices.Clone(gates), func(held boxapi.OwnedGate) bool { return held.Owner == owner })
}

func (t *Table) PutShield(ctx context.Context, shield boxapi.Shield) (boxapi.Shield, error) {
	if err := RefuseInvalid(boxapi.RoutingTable{Shields: []boxapi.Shield{shield}}); err != nil {
		return boxapi.Shield{}, err
	}
	var held boxapi.Shield
	err := t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		if at := slices.IndexFunc(state.Shields, func(held boxapi.Shield) bool { return held.Hostname == shield.Hostname }); at >= 0 && state.Shields[at].Owner != shield.Owner {
			return boxapi.RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
				"%s is shielded on this box for %s\nUnbind it there before %s binds it",
				shield.Hostname, state.Shields[at].Owner, shield.Owner)
		}
		state.Shields = MergeShield(state.Shields, shield)
		held = state.Shields[len(state.Shields)-1]
		return state, nil
	})
	return held, err
}

func (t *Table) RemoveShield(ctx context.Context, hostname, owner string) error {
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		state.Shields = DropShields(state.Shields, func(shield boxapi.Shield) bool {
			return shield.Hostname == hostname && shield.Owner == owner
		})
		return state, nil
	})
}

func (t *Table) PreviewEntry(ctx context.Context) (string, error) {
	state, err := t.Read(ctx)
	if err != nil {
		return "", err
	}
	return state.PreviewBase, nil
}

func (t *Table) InstallPreviewEntry(ctx context.Context, base string) error {
	return t.installPreviewEntry(ctx, base, nil)
}

func (t *Table) installPreviewEntry(ctx context.Context, base string, through func(boxapi.RoutingTable) error) error {
	if err := RefuseInvalidPreviewBase(base); err != nil {
		return err
	}
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		if through != nil {
			if err := through(state); err != nil {
				return boxapi.RoutingTable{}, err
			}
		}
		if state.PreviewBase != "" && state.PreviewBase != base {
			return boxapi.RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
				"this box already answers previews on %s, not %s\nRun `ocel preview rm` and `ocel domain release --preview` first",
				edge.PreviewWildcard(state.PreviewBase), edge.PreviewWildcard(base))
		}
		state.PreviewBase, state.PreviewTunneled = base, through != nil
		return state, nil
	})
}

func (t *Table) RemovePreviewEntry(ctx context.Context, base string) error {
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		if state.PreviewBase != base {
			return state, nil
		}
		if claimed := claimedUnder(state.Claims, base); len(claimed) > 0 {
			return boxapi.RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
				"this box still claims %s under %s\nRun `ocel preview rm` first",
				strings.Join(claimed, ", "), edge.PreviewWildcard(base))
		}
		state.Pins = slices.DeleteFunc(slices.Clone(state.Pins), func(pin boxapi.Pin) bool {
			return isPreviewEntryPin(pin, base) && !hasRowsOf(state, pin.Project)
		})
		state.PreviewBase, state.PreviewTunneled = "", false
		return state, nil
	})
}

func claimedUnder(claims []boxapi.Claim, base string) []string {
	under := "." + strings.ToLower(base)
	claimed := make([]string, 0, len(claims))
	for _, claim := range claims {
		if strings.HasSuffix(strings.ToLower(claim.Hostname), under) {
			claimed = append(claimed, claim.Hostname)
		}
	}
	slices.Sort(claimed)
	return claimed
}

func (t *Table) Read(ctx context.Context) (boxapi.RoutingTable, error) {
	current, err := t.CurrentTable(ctx)
	if err != nil {
		return boxapi.RoutingTable{}, err
	}
	return ReadRoutingTable(current.Table)
}

func (t *Table) ProxyInspected(ctx context.Context, tier environment.Tier) (bool, error) {
	current, err := t.currentPair(ctx)
	if err != nil {
		return false, err
	}
	stale := false
	if current.Table != nil {
		table, err := ReadRoutingTable(current.Table)
		if err != nil {
			return false, err
		}
		rendered, err := RenderProxyConfig(t.front, table)
		if err != nil {
			return false, err
		}
		stale = current.Config != nil && !bytes.Equal(current.Config, rendered)
	}
	declared := t.front.Unrendered(current.Config, SwitchboardPermission)
	if declared == "" {
		return stale, nil
	}
	return false, refusal.Refuse(refusal.CodeInvalid,
		"%s on %s declares %s, which ocel never renders\n"+
			"Remove %s and run `%s` to render it again from %s",
		boxapi.ProxyConfig, t.box.Named(), declared, boxapi.ProxyConfig, provider.BootstrapCommand(tier), boxapi.RoutingTablePath)
}

func (t *Table) Reshape(ctx context.Context, change func(boxapi.RoutingTable) (boxapi.RoutingTable, error)) error {
	return t.recomposed(ctx, func(current boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		changed, err := change(current)
		if err != nil {
			return boxapi.RoutingTable{}, err
		}
		own, err := t.pins(ctx)
		if err != nil {
			return boxapi.RoutingTable{}, err
		}
		changed.Pins = replacePins(changed.Pins, t.project, own)
		return changed, nil
	})
}

func (t *Table) Unpin(ctx context.Context, project string) error {
	return t.recomposed(ctx, func(current boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		if hasRowsOf(current, project) {
			return current, nil
		}
		current.Pins = slices.DeleteFunc(slices.Clone(current.Pins), func(pin boxapi.Pin) bool {
			return pin.Project == project && !isPreviewEntryPin(pin, current.PreviewBase)
		})
		return current, nil
	})
}

func isPreviewEntryPin(pin boxapi.Pin, base string) bool {
	return base != "" && certs.Covering([]boxapi.Pin{pin}, edge.ProbeHostname(edge.PreviewWildcard(base))) != ""
}

func replacePins(held []boxapi.Pin, project string, own []boxapi.Pin) []boxapi.Pin {
	kept := slices.DeleteFunc(slices.Clone(held), func(pin boxapi.Pin) bool { return pin.Project == project })
	for _, pin := range own {
		pin.Project = project
		kept = append(kept, pin)
	}
	return kept
}

func hasRowsOf(state boxapi.RoutingTable, project string) bool {
	surfaces := []string{
		boxapi.Surface(project, string(environment.TierProduction)),
		boxapi.Surface(project, string(environment.TierPreview)),
	}
	owns := func(owner string) bool { return slices.Contains(surfaces, owner) }
	return slices.ContainsFunc(state.Claims, func(claim boxapi.Claim) bool { return owns(claim.Owner) }) ||
		slices.ContainsFunc(state.Routes, func(route boxapi.Route) bool { return owns(route.Owner) }) ||
		slices.ContainsFunc(state.Shields, func(shield boxapi.Shield) bool { return owns(shield.Owner) }) ||
		slices.ContainsFunc(state.Gates, func(held boxapi.OwnedGate) bool { return owns(held.Owner) })
}

func (t *Table) Rerender(ctx context.Context) error {
	return t.recomposed(ctx, func(current boxapi.RoutingTable) (boxapi.RoutingTable, error) { return current, nil })
}

func (t *Table) recomposed(ctx context.Context, compose func(boxapi.RoutingTable) (boxapi.RoutingTable, error)) error {
	elevation, err := ssh.Docker, t.box.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	shaped, err := t.Compose(ctx, compose)
	if err != nil || !shaped.Changed && !shaped.reloading {
		return err
	}
	return t.TakenUp(ctx, shaped, true, elevation)
}

func (t *Table) TakenUp(ctx context.Context, shaped Composed, loading bool, elevation ssh.Acting) error {
	err := shaped.FailedPlace
	if err == nil {
		err = t.serving(ctx, loading, shaped.reloading, shaped.served, elevation)
	}
	if err != nil {
		return t.reverted(ctx, shaped, loading, err, elevation)
	}
	return nil
}

func (t *Table) serving(ctx context.Context, loading, reloading bool, served proxy.Spec, elevation ssh.Acting) error {
	if loading {
		if _, err := t.box.Run(ctx, elevation, "load the switchboard onto "+boxapi.RoutingTablePath, shell.Words(SwitchboardCommand("load", boxapi.RoutingTablePath)), nil); err != nil {
			return err
		}
	}
	if !reloading {
		return nil
	}
	return t.front.Reload(ctx, served)
}

func (t *Table) reverted(ctx context.Context, shaped Composed, loading bool, why error, elevation ssh.Acting) error {
	written := Files(t.front.File())
	if _, failedPlace, err := t.writePair(ctx, shaped.written, shaped.restoring, shaped.reloading); err != nil || failedPlace != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"serving %s failed: %v\nputting back %s also failed: %v",
			written, why, written, errors.Join(err, failedPlace))
	}
	if err := t.serving(ctx, loading, shaped.reloading, shaped.admitted, elevation); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"serving %s failed: %v\nput back %s, but serving again failed too, so the box may still serve routes nothing records any more: %v\nRun the deploy again",
			written, why, written, err)
	}
	return why
}

var ErrTunnelReleased = errors.New("the tunnel was released")

func RefuseUnreserved(state boxapi.RoutingTable, name, named string) error {
	if state.Tunnel == nil || state.Tunnel.Name != name {
		return releasedTunnel{refusal.Refuse(refusal.CodeBusy,
			"the tunnel %s no longer reaches %s: another run released it while this one claimed through it\nDeploy again", name, named)}
	}
	return nil
}

type releasedTunnel struct{ error }

func (r releasedTunnel) Unwrap() error { return r.error }

func (releasedTunnel) Is(target error) bool { return target == ErrTunnelReleased }

func (t *Table) ClaimThrough(ctx context.Context, tunnelName string, claims []boxapi.Claim) error {
	if len(claims) == 0 {
		return nil
	}
	through := make([]boxapi.Claim, 0, len(claims))
	for _, claim := range claims {
		claim.Tunneled = true
		through = append(through, claim)
	}
	if err := RefuseInvalid(boxapi.RoutingTable{Claims: through}); err != nil {
		return err
	}
	return t.Reshape(ctx, func(state boxapi.RoutingTable) (boxapi.RoutingTable, error) {
		if err := RefuseUnreserved(state, tunnelName, t.box.Named()); err != nil {
			return boxapi.RoutingTable{}, err
		}
		for _, claim := range through {
			taken, err := Claim(state.Claims, claim)
			if err != nil {
				return boxapi.RoutingTable{}, err
			}
			state.Claims = taken
		}
		return state, nil
	})
}

func (t *Table) InstallPreviewEntryThrough(ctx context.Context, base, tunnelName string) error {
	return t.installPreviewEntry(ctx, base, func(state boxapi.RoutingTable) error {
		return RefuseUnreserved(state, tunnelName, t.box.Named())
	})
}

func (t *Table) IsTunneled(ctx context.Context, hostname string) (bool, error) {
	state, err := t.Read(ctx)
	if err != nil {
		return false, err
	}
	return state.IsTunneled(hostname), nil
}

func (t *Table) ReadMovedTable(ctx context.Context) (boxapi.RoutingTable, error) {
	pair, err := t.currentPair(ctx)
	if err != nil || pair.Table == nil {
		return boxapi.RoutingTable{}, err
	}
	return ReadRoutingTable(pair.Table)
}

func (t *Table) PlaceUnserved(ctx context.Context) error {
	at := Destination(t.front)
	if at == "" {
		return nil
	}
	pair, err := t.CurrentTable(ctx)
	if err != nil {
		return err
	}
	table, err := ReadRoutingTable(pair.Table)
	if err != nil {
		return err
	}
	rendered, err := RenderProxyConfig(t.front, table)
	if err != nil {
		return err
	}
	origins, err := RenderOriginFiles(t.front, table)
	if err != nil {
		return err
	}
	return t.replace(ctx, pair.Digest(), at, rendered, origins)
}
