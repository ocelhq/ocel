package host

import (
	"bytes"
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit"
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

func (h *Host) ClaimHost(ctx context.Context, claim HostClaim) error {
	if err := validClaim(claim); err != nil {
		return err
	}
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
		taken, err := Claiming(state.Claims, claim)
		if err != nil {
			return ProxyState{}, err
		}
		state.Claims = taken
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

func (h *Host) DisclaimSurface(ctx context.Context, owner string) error {
	return h.reshape(ctx, func(state ProxyState) (ProxyState, error) {
		state.Claims = Disclaiming(state.Claims, func(claim HostClaim) bool { return claim.Owner == owner })
		return state, nil
	})
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
