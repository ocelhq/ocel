package providerkit

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (h *handlers) ResolveImageRegistry(ctx context.Context, req *contractv1.ResolveImageRegistryRequest) (*contractv1.ResolveImageRegistryResponse, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	hosting, hosts := provider.(ImageRegistry)
	if !hosts {
		return nil, connect.NewError(connect.CodeUnimplemented,
			errors.New("this provider hosts no image registry of its own, so images are pushed only where the project names a registry"))
	}
	target, err := hosting.ImageRegistry(ctx, req.GetRepositories())
	if err != nil {
		return nil, RefusalError(err)
	}
	if target.Server == "" {
		return nil, connect.NewError(connect.CodeInternal,
			errors.New("the provider answered an image registry with no server, which names nowhere to push to"))
	}
	return &contractv1.ResolveImageRegistryResponse{
		Server:    target.Server,
		Namespace: target.Namespace,
		Username:  target.Username,
		Password:  target.Password,
	}, nil
}
