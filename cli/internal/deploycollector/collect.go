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

type Prepared struct {
	discovery   discovery.Prepared
	fingerprint string
}

func (p Prepared) Fingerprint() string { return p.fingerprint }

func Prepare(cfg *projectconfig.Config) (Prepared, error) {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return Prepared{}, err
	}

	prepared, err := discovery.Prepare(cfg.Dir, roots)
	if err != nil {
		return Prepared{}, err
	}
	if prepared.Entry() == "" {
		return Prepared{discovery: prepared}, nil
	}

	fingerprint, err := declcache.ContentHash(prepared.Entry())
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{discovery: prepared, fingerprint: fingerprint}, nil
}

func PrepareAndCollect(ctx context.Context, cfg *projectconfig.Config, gate *envgate.Gate, stdout, stderr io.Writer) ([]declare.Resource, error) {
	prepared, err := Prepare(cfg)
	if err != nil {
		return nil, err
	}
	return Collect(ctx, cfg, gate, prepared, stdout, stderr)
}

func Collect(ctx context.Context, cfg *projectconfig.Config, gate *envgate.Gate, prepared Prepared, stdout, stderr io.Writer) ([]declare.Resource, error) {
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

	if err := discovery.Run(ctx, cfg.Dir, prepared.discovery, collectorAddr, stdout, stderr); err != nil {
		return nil, err
	}

	return c.Snapshot(), nil
}
