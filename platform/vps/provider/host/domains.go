package host

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
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
	held, err := h.routingDocument(ctx)
	if err != nil {
		return RoutingTable{}, err
	}
	return ReadRoutingTable([]byte(held.text))
}

func (h *Host) proxyRendered(ctx context.Context, class providerkit.Class) error {
	table, err := h.routingTable(ctx)
	if err != nil {
		return err
	}
	want, err := RenderProxyConfig(table)
	if err != nil {
		return err
	}
	held, err := h.reach(ctx, "read "+ProxyConfig, "cat "+quoted(ProxyConfig), nil)
	if err != nil {
		return err
	}
	if held != string(want) {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"%s on %s is not what ocel renders from %s, so the proxy may serve what no deploy wrote\n"+
				"Put back what ocel wrote, or remove %s and run `%s` to start this box's routes over",
			ProxyConfig, h.named(), live.RoutingTable, live.RoutingTable, providerkit.BootstrapCommand(class))
	}
	return nil
}

func (h *Host) reshape(ctx context.Context, change func(RoutingTable) (RoutingTable, error)) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	shaped, err := h.composeProxy(ctx, func(standing RoutingTable) (RoutingTable, error) {
		changed, err := change(standing)
		if err != nil {
			return RoutingTable{}, err
		}
		if changed.Pins, err = h.VerifiedPins(ctx); err != nil {
			return RoutingTable{}, err
		}
		return changed, nil
	})
	if err != nil || !shaped.changed {
		return err
	}
	if _, err := h.ran(ctx, "reload the proxy",
		words(helperCommand("flip", ProxyConfigMount)), nil, elevation); err != nil {
		return h.reverted(ctx, shaped.held, shaped.written, err)
	}
	return nil
}

func (h *Host) reverted(ctx context.Context, previous RoutingTable, expected string, why error) error {
	if _, err := h.writeRouting(ctx, expected, previous); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"the running proxy rejected %s: %v\nrestoring %s and %s also failed: %v",
			ProxyConfig, why, live.RoutingTable, ProxyConfig, err)
	}
	return why
}
