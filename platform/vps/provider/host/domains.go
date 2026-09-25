package host

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
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
	if err := validRoute(route); err != nil {
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
	for _, claim := range claims {
		if err := validClaim(claim); err != nil {
			return err
		}
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
	if err := PreviewBaseUsable(base); err != nil {
		return err
	}
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		if state.PreviewBase != "" && state.PreviewBase != base {
			return RoutingTable{}, providerkit.Refuse(providerkit.CodeBusy,
				"this box already answers previews on %s, not %s\nRun `ocel preview rm` and `ocel domain release --preview` first",
				edge.PreviewWildcard(state.PreviewBase), edge.PreviewWildcard(base))
		}
		state.PreviewBase = base
		return state, nil
	})
}

func (h *Host) RemovePreviewEntry(ctx context.Context, base string) error {
	return h.reshape(ctx, func(state RoutingTable) (RoutingTable, error) {
		if state.PreviewBase != base {
			return state, nil
		}
		if held := claimedUnder(state.Claims, base); len(held) > 0 {
			return RoutingTable{}, providerkit.Refuse(providerkit.CodeBusy,
				"this box still claims %s under %s\nRun `ocel preview rm` first",
				strings.Join(held, ", "), edge.PreviewWildcard(base))
		}
		state.PreviewBase = ""
		return state, nil
	})
}

func claimedUnder(claims []HostClaim, base string) []string {
	under := "." + strings.ToLower(base)
	held := make([]string, 0, len(claims))
	for _, claim := range claims {
		if strings.HasSuffix(strings.ToLower(claim.Hostname), under) {
			held = append(held, claim.Hostname)
		}
	}
	slices.Sort(held)
	return held
}

func (h *Host) routingTable(ctx context.Context) (RoutingTable, error) {
	held, err := h.tableHeld(ctx)
	if err != nil {
		return RoutingTable{}, err
	}
	return ReadRoutingTable(held.table)
}

func (h *Host) proxyInspected(ctx context.Context, class providerkit.Class) (bool, error) {
	held, err := h.pairHeld(ctx)
	if err != nil {
		return false, err
	}
	stale := false
	if held.table != nil {
		table, err := ReadRoutingTable(held.table)
		if err != nil {
			return false, err
		}
		rendered, err := RenderProxyConfig(table)
		if err != nil {
			return false, err
		}
		stale = held.config != nil && !bytes.Equal(held.config, rendered)
	}
	declared := caddy.Foreign(held.config)
	if declared == "" {
		return stale, nil
	}
	return false, providerkit.Refuse(providerkit.CodeInvalid,
		"%s on %s declares %s, which ocel never renders\n"+
			"Remove %s and run `%s` to render it again from %s",
		ProxyConfig, h.named(), declared, ProxyConfig, providerkit.BootstrapCommand(class), live.RoutingTable)
}

func (h *Host) reshape(ctx context.Context, change func(RoutingTable) (RoutingTable, error)) error {
	return h.recomposed(ctx, func(standing RoutingTable) (RoutingTable, error) {
		changed, err := change(standing)
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
	return h.recomposed(ctx, func(standing RoutingTable) (RoutingTable, error) { return standing, nil })
}

func (h *Host) recomposed(ctx context.Context, compose func(RoutingTable) (RoutingTable, error)) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	shaped, err := h.composeRouting(ctx, compose)
	if err != nil || !shaped.changed {
		return err
	}
	if err := h.served(ctx, shaped.is, shaped.admitting, elevation); err != nil {
		return h.reverted(ctx, shaped, err, elevation)
	}
	return nil
}

func (h *Host) served(ctx context.Context, table RoutingTable, admitting bool, elevation string) error {
	if _, err := h.ran(ctx, "load the switchboard onto "+live.RoutingTable,
		words(switchboardCommand("load", live.RoutingTable)), nil, elevation); err != nil {
		return err
	}
	if !admitting {
		return nil
	}
	return h.front.Admit(ctx, admission(table))
}

func (h *Host) reverted(ctx context.Context, shaped composed, why error, elevation string) error {
	if _, err := h.writePair(ctx, shaped.written, shaped.prior); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"serving %s failed: %v\nrestoring %s and %s also failed: %v",
			live.RoutingTable, why, live.RoutingTable, ProxyConfig, err)
	}
	if err := h.served(ctx, shaped.was, shaped.admitting, elevation); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"serving %s failed: %v\n%s and %s were restored, but serving them again failed too, so the box may still serve what they no longer record: %v\nRun the deploy again",
			live.RoutingTable, why, live.RoutingTable, ProxyConfig, err)
	}
	return why
}
