package host

import (
	"context"
	"encoding/json"
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
	held, err := h.tableHeld(ctx)
	if err != nil {
		return RoutingTable{}, err
	}
	return ReadRoutingTable(held.table)
}

func (h *Host) proxyInspected(ctx context.Context, class providerkit.Class) error {
	held, err := h.pairHeld(ctx)
	if err != nil {
		return err
	}
	if held.table != nil {
		if _, err := ReadRoutingTable(held.table); err != nil {
			return err
		}
	}
	if !onDemand(held.config) {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s on %s declares a tls automation policy ocel never renders, so the proxy may order certificates for any name it is asked for\n"+
			"Remove %s and run `%s` to render it again from %s",
		ProxyConfig, h.named(), ProxyConfig, providerkit.BootstrapCommand(class), live.RoutingTable)
}

func onDemand(config []byte) bool {
	var read struct {
		Apps struct {
			TLS struct {
				Automation json.RawMessage `json:"automation"`
			} `json:"tls"`
		} `json:"apps"`
	}
	return json.Unmarshal(config, &read) == nil && len(read.Apps.TLS.Automation) > 0
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
	if _, err := h.ran(ctx, "reload the proxy",
		words(helperCommand("flip", ProxyConfigMount)), nil, elevation); err != nil {
		return h.reverted(ctx, shaped, err, elevation)
	}
	return nil
}

func (h *Host) reverted(ctx context.Context, shaped composed, why error, elevation string) error {
	if _, err := h.writePair(ctx, shaped.written, shaped.prior); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"reloading the proxy onto %s failed: %v\nrestoring %s and %s also failed: %v",
			ProxyConfig, why, live.RoutingTable, ProxyConfig, err)
	}
	if _, err := h.ran(ctx, "reload the proxy onto what it served before",
		words(helperCommand("flip", ProxyConfigMount)), nil, elevation); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"reloading the proxy onto %s failed: %v\n%s and %s were restored, but reloading the proxy onto them failed too, so it may still serve what they no longer record: %v\nRun the deploy again",
			ProxyConfig, why, live.RoutingTable, ProxyConfig, err)
	}
	return why
}
