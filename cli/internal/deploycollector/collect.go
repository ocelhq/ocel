package deploycollector

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/declcache"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func Collect(ctx context.Context, cfg *projectconfig.Config, gate *envgate.Gate, stdout, stderr io.Writer) ([]declare.Resource, error) {
	roots, err := discovery.Roots(cfg.Dir, cfg.Discovery.Paths, cfg.AppPaths())
	if err != nil {
		return nil, err
	}

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

	if err := discovery.Run(ctx, cfg.Dir, roots, collectorAddr, stdout, stderr); err != nil {
		return nil, err
	}

	return c.Snapshot(), nil
}

func Fingerprint(cfg *projectconfig.Config) (string, error) {
	roots, err := discovery.Roots(cfg.Dir, cfg.Discovery.Paths, cfg.AppPaths())
	if err != nil {
		return "", err
	}

	entry, err := discovery.BundleRoots(cfg.Dir, roots)
	if err != nil {
		return "", err
	}
	return declcache.ContentHash(entry)
}
