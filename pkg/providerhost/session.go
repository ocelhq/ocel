package providerhost

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
)

func (h *Host) open(caller endpoint.Caller, key, id, tenant string, named bool, slug string, tier environment.Tier) (*session, error) {
	dir, err := os.MkdirTemp(h.config.Scratch, "session-")
	if err != nil {
		return nil, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &session{key: key, id: id, tenant: tenant, named: named, dir: dir, lastUsed: h.config.Now(), ready: make(chan struct{}), answering: map[string]bool{}}
	s.inner = providerserver.NewSession(providerserver.Config{
		Version: h.config.Version,
		New: func(ctx context.Context, settings provider.Settings) (provider.Provider, error) {
			return h.config.New(ctx, caller, id, settings)
		},
		Slug:            slug,
		ServesSlugAlone: !named,
		Tier:            tier,
	})
	mux := http.NewServeMux()
	if named {
		mux.Handle(contractv1connect.ProviderServiceConfigureProcedure, connect.NewUnaryHandler(
			contractv1connect.ProviderServiceConfigureProcedure,
			func(ctx context.Context, request *connect.Request[contractv1.ConfigureRequest]) (*connect.Response[contractv1.ConfigureResponse], error) {
				return h.configureNamed(ctx, s, request)
			},
		))
	}
	mux.Handle(contractv1connect.ProviderServiceConfirmProcedure, connect.NewUnaryHandler(
		contractv1connect.ProviderServiceConfirmProcedure,
		func(ctx context.Context, request *connect.Request[contractv1.ConfirmRequest]) (*connect.Response[contractv1.ConfirmResponse], error) {
			return h.confirm(ctx, s, request)
		},
	))
	mux.Handle("/", s.inner.Handler())
	s.mux = mux
	return s, nil
}

func (h *Host) configureInDirectory(ctx context.Context, s *session, settings provider.Settings, edge *contractv1.EdgeSelection) (*contractv1.ProviderFacts, error) {
	settings.ProjectDir = s.dir
	facts, err := s.inner.Configure(ctx, settings, edge)
	if err != nil {
		var wire *connect.Error
		if !errors.As(err, &wire) {
			err = provider.RefusalError(err)
		}
		return nil, err
	}
	return facts, nil
}
