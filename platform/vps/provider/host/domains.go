package host

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func (h *Host) Claims(ctx context.Context) ([]HostClaim, error) {
	state, err := h.routingTable(ctx)
	if err != nil {
		return nil, err
	}
	return state.Claims, nil
}

func (h *Host) Serving(ctx context.Context, key RouteKey) (string, error) {
	state, err := h.routingTable(ctx)
	if err != nil {
		return "", err
	}
	at := slices.IndexFunc(state.Routes, func(route AppRoute) bool { return route.RouteKey == key })
	if at < 0 {
		return "", nil
	}
	return state.Routes[at].Upstream, nil
}

func (h *Host) RouteResource(ctx context.Context, route AppRoute) error {
	if err := validTable(RoutingTable{Routes: []AppRoute{route}}); err != nil {
		return err
	}
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Routes = Routing(state.Routes, route)
		return state, nil
	})
}

func (h *Host) UnrouteApp(ctx context.Context, key RouteKey) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Routes = Unrouting(state.Routes, func(route AppRoute) bool { return route.RouteKey == key })
		state.Claims = Disclaiming(state.Claims, func(claim HostClaim) bool {
			return claim.Owner == key.Owner && claim.Pointer == key.Pointer && claim.App == key.App
		})
		return state, nil
	})
}

func (h *Host) UnroutePointer(ctx context.Context, owner, pointer string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Routes = Unrouting(state.Routes, func(route AppRoute) bool {
			return route.Owner == owner && route.Pointer == pointer
		})
		return state, nil
	})
}

func (h *Host) UnrouteSurface(ctx context.Context, owner string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Routes = Unrouting(state.Routes, func(route AppRoute) bool { return route.Owner == owner })
		return state, nil
	})
}

func (h *Host) ClaimHosts(ctx context.Context, claims []HostClaim) error {
	if err := validTable(RoutingTable{Claims: claims}); err != nil {
		return err
	}
	if len(claims) == 0 {
		return nil
	}
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		for _, claim := range claims {
			taken, err := Claiming(state.Claims, claim)
			if err != nil {
				return RoutingTable{}, err
			}
			state.Claims = taken
		}
		return state, nil
	})
}

func (h *Host) DisclaimHost(ctx context.Context, hostname, owner string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Claims = Disclaiming(state.Claims, func(claim HostClaim) bool {
			return claim.Hostname == hostname && claim.Owner == owner
		})
		return state, nil
	})
}

func (h *Host) DisclaimPointer(ctx context.Context, owner, pointer string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Claims = Disclaiming(state.Claims, func(claim HostClaim) bool {
			return claim.Owner == owner && claim.Pointer == pointer
		})
		return state, nil
	})
}

func (h *Host) DisclaimSurface(ctx context.Context, owner string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Claims = Disclaiming(state.Claims, func(claim HostClaim) bool { return claim.Owner == owner })
		state.Shields = DropShields(state.Shields, func(shield Shield) bool { return shield.Owner == owner })
		state.Gates = dropGates(state.Gates, owner)
		return state, nil
	})
}

func (h *Host) PutGate(ctx context.Context, owner string, held *gate.Gate) error {
	if held != nil {
		if err := validTable(RoutingTable{Gates: []OwnedGate{{Owner: owner, Gate: *held}}}); err != nil {
			return err
		}
	}
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Gates = dropGates(state.Gates, owner)
		if held != nil {
			state.Gates = append(state.Gates, OwnedGate{Owner: owner, Gate: *held})
		}
		return state, nil
	})
}

func dropGates(gates []OwnedGate, owner string) []OwnedGate {
	return slices.DeleteFunc(slices.Clone(gates), func(held OwnedGate) bool { return held.Owner == owner })
}

func (h *Host) PutShield(ctx context.Context, shield Shield) (Shield, error) {
	if err := validTable(RoutingTable{Shields: []Shield{shield}}); err != nil {
		return Shield{}, err
	}
	var held Shield
	err := h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		if at := slices.IndexFunc(state.Shields, func(held Shield) bool { return held.Hostname == shield.Hostname }); at >= 0 && state.Shields[at].Owner != shield.Owner {
			return RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
				"%s is shielded on this box for %s\nUnbind it there before %s binds it",
				shield.Hostname, state.Shields[at].Owner, shield.Owner)
		}
		state.Shields = MergeShield(state.Shields, shield)
		held = state.Shields[len(state.Shields)-1]
		return state, nil
	})
	return held, err
}

func (h *Host) RemoveShield(ctx context.Context, hostname, owner string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		state.Shields = DropShields(state.Shields, func(shield Shield) bool {
			return shield.Hostname == hostname && shield.Owner == owner
		})
		return state, nil
	})
}

func (h *Host) PreviewEntry(ctx context.Context) (string, error) {
	state, err := h.routingTable(ctx)
	if err != nil {
		return "", err
	}
	return state.PreviewBase, nil
}

func (h *Host) InstallPreviewEntry(ctx context.Context, base string) error {
	return h.installPreviewEntry(ctx, base, nil)
}

func (h *Host) installPreviewEntry(ctx context.Context, base string, through func(RoutingTable) error) error {
	if err := RefuseInvalidPreviewBase(base); err != nil {
		return err
	}
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		if through != nil {
			if err := through(state); err != nil {
				return RoutingTable{}, err
			}
		}
		if state.PreviewBase != "" && state.PreviewBase != base {
			return RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
				"this box already answers previews on %s, not %s\nRun `ocel preview rm` and `ocel domain release --preview` first",
				edge.PreviewWildcard(state.PreviewBase), edge.PreviewWildcard(base))
		}
		state.PreviewBase, state.PreviewTunneled = base, through != nil
		return state, nil
	})
}

func (h *Host) RemovePreviewEntry(ctx context.Context, base string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		if state.PreviewBase != base {
			return state, nil
		}
		if claimed := claimedUnder(state.Claims, base); len(claimed) > 0 {
			return RoutingTable{}, refusal.Refuse(refusal.CodeBusy,
				"this box still claims %s under %s\nRun `ocel preview rm` first",
				strings.Join(claimed, ", "), edge.PreviewWildcard(base))
		}
		state.PreviewBase, state.PreviewTunneled = "", false
		return state, nil
	})
}

func claimedUnder(claims []HostClaim, base string) []string {
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

func (h *Host) routingTable(ctx context.Context) (RoutingTable, error) {
	current, err := h.currentTable(ctx)
	if err != nil {
		return RoutingTable{}, err
	}
	return ReadRoutingTable(current.table)
}

func (h *Host) proxyInspected(ctx context.Context, tier environment.Tier) (bool, error) {
	current, err := h.currentPair(ctx)
	if err != nil {
		return false, err
	}
	stale := false
	if current.table != nil {
		table, err := ReadRoutingTable(current.table)
		if err != nil {
			return false, err
		}
		rendered, err := RenderProxyConfig(h.front, table)
		if err != nil {
			return false, err
		}
		stale = current.config != nil && !bytes.Equal(current.config, rendered)
	}
	declared := h.front.Unrendered(current.config, SwitchboardPermission)
	if declared == "" {
		return stale, nil
	}
	return false, refusal.Refuse(refusal.CodeInvalid,
		"%s on %s declares %s, which ocel never renders\n"+
			"Remove %s and run `%s` to render it again from %s",
		ProxyConfig, h.Named(), declared, ProxyConfig, provider.BootstrapCommand(tier), boxapi.RoutingTablePath)
}

func (h *Host) reshape(ctx context.Context, change func(RoutingTable) (RoutingTable, error)) error {
	return h.recomposed(ctx, func(current RoutingTable) (RoutingTable, error) {
		changed, err := change(current)
		if err != nil {
			return RoutingTable{}, err
		}
		if changed.Pins, err = h.VerifiedPins(ctx); err != nil {
			return RoutingTable{}, err
		}
		return changed, nil
	})
}

func (h *Host) rerender(ctx context.Context) error {
	return h.recomposed(ctx, func(current RoutingTable) (RoutingTable, error) { return current, nil })
}

func (h *Host) recomposed(ctx context.Context, compose func(RoutingTable) (RoutingTable, error)) error {
	elevation, err := ssh.Docker, h.Assume(ctx, ssh.Docker)
	if err != nil {
		return err
	}
	shaped, err := h.composeRouting(ctx, compose)
	if err != nil || !shaped.changed && !shaped.reloading {
		return err
	}
	return h.takenUp(ctx, shaped, true, elevation)
}

func (h *Host) takenUp(ctx context.Context, shaped composed, loading bool, elevation ssh.Acting) error {
	err := shaped.failedPlace
	if err == nil {
		err = h.serving(ctx, loading, shaped.reloading, shaped.served, elevation)
	}
	if err != nil {
		return h.reverted(ctx, shaped, loading, err, elevation)
	}
	return nil
}

func (h *Host) serving(ctx context.Context, loading, reloading bool, served proxy.Spec, elevation ssh.Acting) error {
	if loading {
		if _, err := h.Run(ctx, elevation, "load the switchboard onto "+boxapi.RoutingTablePath, shell.Words(switchboardCommand("load", boxapi.RoutingTablePath)), nil); err != nil {
			return err
		}
	}
	if !reloading {
		return nil
	}
	return h.front.Reload(ctx, served)
}

func (h *Host) reverted(ctx context.Context, shaped composed, loading bool, why error, elevation ssh.Acting) error {
	written := routingFiles(h.front.File())
	if _, failedPlace, err := h.writePair(ctx, shaped.written, shaped.restoring, shaped.reloading); err != nil || failedPlace != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"serving %s failed: %v\nputting back %s also failed: %v",
			written, why, written, errors.Join(err, failedPlace))
	}
	if err := h.serving(ctx, loading, shaped.reloading, shaped.admitted, elevation); err != nil {
		return refusal.Refuse(refusal.CodeNotReady,
			"serving %s failed: %v\nput back %s, but serving again failed too, so the box may still serve routes nothing records any more: %v\nRun the deploy again",
			written, why, written, err)
	}
	return why
}
