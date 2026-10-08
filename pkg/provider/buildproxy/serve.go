package buildproxy

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
)

func Serve(ctx context.Context, req provider.BindingProxyRequest, served []provider.BindingType, open func(ctx context.Context, bindings []provider.Binding) (provider.BindingProxy, error)) (provider.BindingProxy, error) {
	var bound []provider.Binding
	var unserved []string
	for _, binding := range req.Bindings {
		if slices.Contains(served, binding.Type) {
			bound = append(bound, binding)
			continue
		}
		unserved = append(unserved, binding.Name)
	}
	if len(bound) == 0 {
		return provider.BindingProxy{Unserved: unserved}, nil
	}
	proxy, err := open(ctx, bound)
	if err != nil {
		return provider.BindingProxy{}, err
	}
	proxy.Unserved = unserved
	return proxy, nil
}
