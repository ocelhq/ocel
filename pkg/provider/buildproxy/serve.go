package buildproxy

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
)

func Serve(ctx context.Context, req provider.BindingProxyRequest, served []provider.BindingType, open func(ctx context.Context, grants []provider.BindingGrant) (provider.BindingProxy, error)) (provider.BindingProxy, error) {
	var grants []provider.BindingGrant
	var unserved []string
	for _, grant := range req.Grants {
		var bound []provider.Binding
		for _, binding := range grant.Bindings {
			if slices.Contains(served, binding.Type) {
				bound = append(bound, binding)
				continue
			}
			if !slices.Contains(unserved, binding.Name) {
				unserved = append(unserved, binding.Name)
			}
		}
		if len(bound) > 0 {
			grants = append(grants, provider.BindingGrant{Grantee: grant.Grantee, Bindings: bound})
		}
	}
	if len(grants) == 0 {
		return provider.BindingProxy{Unserved: unserved}, nil
	}
	proxy, err := open(ctx, grants)
	if err != nil {
		return provider.BindingProxy{}, err
	}
	proxy.Unserved = unserved
	return proxy, nil
}

func ServeGrants(grants []bindingproxy.Grant, report func(error)) (provider.BindingProxy, error) {
	served, err := bindingproxy.ServeGrants(grants, report)
	if err != nil {
		return provider.BindingProxy{}, err
	}
	sessions := make([]provider.BindingSession, 0, len(served.Sessions))
	for _, session := range served.Sessions {
		sessions = append(sessions, provider.BindingSession{Grantee: session.Grantee, SessionToken: session.Token})
	}
	return provider.BindingProxy{Address: served.Address, Sessions: sessions, Close: func() { _ = served.Close() }}, nil
}
