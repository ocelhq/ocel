package declaration

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/channel"
)

func Prepare(cfg *projectconfig.Config) (discovery.Programs, error) {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return discovery.Programs{}, err
	}
	return discovery.Prepare(cfg.Dir, roots)
}

func Collect(ctx context.Context, cfg *projectconfig.Config, declarations *variables.Declarations, stdout, stderr io.Writer) ([]Resource, error) {
	prepared, err := Prepare(cfg)
	if err != nil {
		return nil, err
	}
	return CollectPrepared(ctx, cfg, declarations, prepared, stdout, stderr)
}

func CollectPrepared(ctx context.Context, cfg *projectconfig.Config, declarations *variables.Declarations, prepared discovery.Programs, stdout, stderr io.Writer) ([]Resource, error) {
	service := NewService(declarations)
	if err := declarations.Prefetch(ctx); err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start the declaration server: %w", err)
	}
	address, token := listener.Addr().String(), channel.NewSessionToken()
	httpSrv := &http.Server{Handler: channel.LoopbackGuard(address, token, collectionMux(service))}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	server := discovery.Server{URL: "http://" + address, Token: token}
	err = discovery.Run(ctx, cfg.Dir, prepared, server, stdout, stderr)
	if refused := service.TakeSDKRefusal(); refused != nil {
		return nil, refused
	}
	if err != nil {
		return nil, err
	}
	return service.Resources(), nil
}

func collectionMux(service *Service) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle(service.Handler())
	mux.HandleFunc("/sync", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
