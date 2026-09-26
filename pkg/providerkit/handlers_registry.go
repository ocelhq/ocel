package providerkit

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
)

func (h *handlers) ResolveImageRegistry(ctx context.Context, req *contractv1.ResolveImageRegistryRequest) (*contractv1.ResolveImageRegistryResponse, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	ensure := provider.Hooks().EnsureImageRegistry
	if ensure == nil {
		return nil, connect.NewError(connect.CodeUnimplemented,
			errors.New("this provider hosts no image registry of its own, so images are pushed only where the project names a registry"))
	}
	class, err := classOf(req.GetTier())
	if err != nil {
		return nil, RefusalError(err)
	}
	target, err := ensure(ctx, class, req.GetRepositories())
	if err != nil {
		return nil, RefusalError(err)
	}
	if !target.Named() {
		if target != (images.Registry{}) {
			return nil, connect.NewError(connect.CodeInternal,
				errors.New("the provider answered an image registry with no server, which names nowhere to push to"))
		}
		return nil, connect.NewError(connect.CodeUnimplemented,
			errors.New("this provider hosts no image registry for this deploy, so images stay where the build left them"))
	}
	return &contractv1.ResolveImageRegistryResponse{
		Server:    target.Server,
		Namespace: target.Namespace,
		Username:  target.Username,
		Password:  target.Password,
	}, nil
}
