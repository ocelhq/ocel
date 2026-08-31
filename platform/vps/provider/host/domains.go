package host

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (h *Host) Claims(ctx context.Context) ([]HostClaim, error) {
	state, _, err := h.proxyState(ctx)
	if err != nil {
		return nil, err
	}
	return state.Claims, nil
}

func (h *Host) Serving(ctx context.Context, key RouteKey) (string, error) {
	state, _, err := h.proxyState(ctx)
	if err != nil {
		return "", err
	}
	at := slices.IndexFunc(state.Routes, func(route AppRoute) bool { return route.RouteKey == key })
	if at < 0 {
		return "", nil
	}
	return state.Routes[at].Upstream, nil
}

func (h *Host) UnroutePointer(ctx context.Context, owner, pointer string) error {
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
		state.Routes = Unrouting(state.Routes, func(route AppRoute) bool {
			return route.Owner == owner && route.Pointer == pointer
		})
		return state, nil
	})
}

func (h *Host) UnrouteSurface(ctx context.Context, owner string) error {
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
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
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
		for _, claim := range claims {
			taken, err := Claiming(state.Claims, claim)
			if err != nil {
				return ProxyState{}, err
			}
			state.Claims = taken
		}
		return state, nil
	})
}

func (h *Host) DisclaimHost(ctx context.Context, hostname, owner string) error {
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
		state.Claims = Disclaiming(state.Claims, func(claim HostClaim) bool {
			return claim.Hostname == hostname && claim.Owner == owner
		})
		return state, nil
	})
}

func (h *Host) DisclaimPointer(ctx context.Context, owner, pointer string) error {
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
		state.Claims = Disclaiming(state.Claims, func(claim HostClaim) bool {
			return claim.Owner == owner && claim.Pointer == pointer
		})
		return state, nil
	})
}

func (h *Host) DisclaimSurface(ctx context.Context, owner string) error {
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
		state.Claims = Disclaiming(state.Claims, func(claim HostClaim) bool { return claim.Owner == owner })
		return state, nil
	})
}

func (h *Host) PreviewEntry(ctx context.Context) (string, error) {
	state, _, err := h.proxyState(ctx)
	if err != nil {
		return "", err
	}
	return state.PreviewBase, nil
}

func (h *Host) InstallPreviewEntry(ctx context.Context, base string) error {
	if err := PreviewBaseUsable(base); err != nil {
		return err
	}
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
		if state.PreviewBase != "" && state.PreviewBase != base {
			return ProxyState{}, providerkit.Refuse(providerkit.CodeBusy,
				"this box already answers previews on %s, and every preview hostname it serves is a name under that base: take those previews down with `ocel preview rm` and release the base with `ocel domain release --preview` first, or raising %s here takes every live preview off the air with nothing telling the projects that lost them",
				edge.PreviewWildcard(state.PreviewBase), edge.PreviewWildcard(base))
		}
		state.PreviewBase = base
		return state, nil
	})
}

func (h *Host) RemovePreviewEntry(ctx context.Context, base string) error {
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
		if state.PreviewBase != base {
			return state, nil
		}
		if held := claimedUnder(state.Claims, base); len(held) > 0 {
			return ProxyState{}, providerkit.Refuse(providerkit.CodeBusy,
				"this box still claims %s under %s: releasing the base takes the catch-all down and leaves every one of those hostnames routed and renewing, and raising a second base beside them installs a second catch-all over sites that are still live. Take them down with `ocel preview rm` first",
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

func (h *Host) proxyState(ctx context.Context) (ProxyState, proxyDocument, error) {
	held, err := h.proxyDocument(ctx)
	if err != nil {
		return ProxyState{}, proxyDocument{}, err
	}
	state, err := ReadProxyState([]byte(held.text))
	if err != nil {
		return ProxyState{}, proxyDocument{}, err
	}
	return state, held, nil
}

func (h *Host) reshape(ctx context.Context, change func(ProxyState) (ProxyState, error)) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	state, held, err := h.proxyState(ctx)
	if err != nil {
		return err
	}
	standing, err := RenderProxyConfig(state)
	if err != nil {
		return err
	}
	changed, err := change(state)
	if err != nil {
		return err
	}
	if changed.Pins, err = h.VerifiedPins(ctx); err != nil {
		return err
	}
	next, err := RenderProxyConfig(changed)
	if err != nil {
		return err
	}
	if bytes.Equal(next, standing) {
		return nil
	}
	written, err := h.writeProxyDocument(ctx, held.digest, string(next))
	if err != nil {
		return err
	}
	if _, err := h.ran(ctx, "load the hostnames this box claims onto the running proxy",
		words(helperCommand("flip", proxyConfigMount)), nil, elevation); err != nil {
		return h.reverted(ctx, held.text, written, err)
	}
	return nil
}

func (h *Host) reverted(ctx context.Context, previous, expected string, why error) error {
	if _, err := h.writeProxyDocument(ctx, expected, previous); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s was written and the running proxy would not take it: %v\n%s could not be put back either, and a restarted proxy serves what this file says: %v",
			ProxyConfig, why, ProxyConfig, err)
	}
	return why
}
