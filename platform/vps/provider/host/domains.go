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

func (h *Host) Serving(ctx context.Context, app string) (string, error) {
	state, _, err := h.proxyState(ctx)
	if err != nil {
		return "", err
	}
	at := slices.IndexFunc(state.Routes, func(route AppRoute) bool { return route.App == app })
	if at < 0 {
		return "", nil
	}
	return state.Routes[at].Upstream, nil
}

func (h *Host) ClaimHost(ctx context.Context, claim HostClaim) error {
	if err := validClaim(claim); err != nil {
		return err
	}
	return h.reshape(ctx, func(state ProxyState) ProxyState {
		state.Claims = Claiming(state.Claims, claim)
		return state
	})
}

func (h *Host) DisclaimHost(ctx context.Context, hostname string) error {
	return h.reshape(ctx, func(state ProxyState) ProxyState {
		state.Claims = Disclaiming(state.Claims, hostname)
		return state
	})
}

func (h *Host) proxyState(ctx context.Context) (ProxyState, string, error) {
	document, err := h.reach(ctx, "read "+ProxyConfig, "cat "+quoted(ProxyConfig), nil)
	if err != nil {
		return ProxyState{}, "", err
	}
	state, err := ReadProxyState([]byte(document))
	if err != nil {
		return ProxyState{}, "", err
	}
	return state, document, nil
}

func (h *Host) reshape(ctx context.Context, change func(ProxyState) ProxyState) error {
	elevation, err := h.reachDocker(ctx)
	if err != nil {
		return err
	}
	state, previous, err := h.proxyState(ctx)
	if err != nil {
		return err
	}
	standing, err := RenderProxyConfig(state)
	if err != nil {
		return err
	}
	next, err := RenderProxyConfig(change(state))
	if err != nil {
		return err
	}
	if bytes.Equal(next, standing) {
		return nil
	}
	if err := h.writeProxyDocument(ctx, string(next)); err != nil {
		return err
	}
	if _, err := h.ran(ctx, "load the hostnames this box claims onto the running proxy",
		words(helperCommand("flip", proxyConfigMount)), nil, elevation); err != nil {
		return h.reverted(ctx, previous, err)
	}
	return nil
}

func (h *Host) reverted(ctx context.Context, previous string, why error) error {
	if err := h.writeProxyDocument(ctx, previous); err != nil {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"%s was written and the running proxy would not take it: %v\n%s could not be put back either, and a restarted proxy serves what this file says: %v",
			ProxyConfig, why, ProxyConfig, err)
	}
	return why
}
