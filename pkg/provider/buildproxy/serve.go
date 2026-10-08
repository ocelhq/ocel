package buildproxy

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
)

func Serve(ctx context.Context, req provider.BindingProxyRequest, served []provider.BindingType, open func(ctx context.Context, bindings []provider.Binding) (bindingproxy.Services, func(), error)) (provider.BindingProxy, error) {
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
	services, release, err := open(ctx, bound)
	if err != nil {
		return provider.BindingProxy{}, err
	}
	listening, err := bindingproxy.Serve(services)
	if err != nil {
		release()
		return provider.BindingProxy{}, err
	}
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		if err := <-listening.Errs; err != nil && !errors.Is(err, http.ErrServerClosed) {
			req.ReportFailure(err)
		}
	}()
	return provider.BindingProxy{
		Address:      listening.Address,
		SessionToken: listening.Token,
		Unserved:     unserved,
		Close: func() {
			_ = listening.Close()
			<-watched
			release()
		},
	}, nil
}
