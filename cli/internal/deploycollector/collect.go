package deploycollector

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func Collect(ctx context.Context, cfg *projectconfig.Config, gate *envgate.Gate, stdout, stderr io.Writer) ([]declare.Resource, error) {
	entry, err := Bundle(cfg)
	if err != nil {
		return nil, err
	}
	return CollectBundled(ctx, cfg, gate, entry, stdout, stderr)
}

func Bundle(cfg *projectconfig.Config) (string, error) {
	files, err := discovery.Discover(cfg.Dir, cfg.Discovery.Paths)
	if err != nil {
		return "", fmt.Errorf("discover resources: %w", err)
	}

	entry, err := discovery.Bundle(cfg.Dir, files)
	if err != nil {
		return "", fmt.Errorf("bundle discovery entrypoint: %w", err)
	}
	return entry, nil
}

func CollectBundled(ctx context.Context, cfg *projectconfig.Config, gate *envgate.Gate, entry string, stdout, stderr io.Writer) ([]declare.Resource, error) {
	c := New(gate)

	if err := gate.Prefetch(ctx); err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start deploy collector: %w", err)
	}
	httpSrv := &http.Server{Handler: c.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	collectorAddr := "http://" + listener.Addr().String()

	if err := discovery.Run(ctx, entry, collectorAddr, stdout, stderr); err != nil {
		return nil, err
	}

	return c.Snapshot(), nil
}
