package deploycollector

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

// Collect runs node discovery (OCEL_PHASE=discovery) over cfg's resolved
// discovery.paths against a fresh Collector, using the same
// discovery.Discover/discovery.Bundle mechanism the dev path uses, and
// returns every resource declared with its full typed config. It never
// starts or talks to the dev server (cli/internal/devserver) and never
// provisions anything.
func Collect(ctx context.Context, cfg *projectconfig.Config, stdout, stderr io.Writer) ([]declare.Resource, error) {
	c := New()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start deploy collector: %w", err)
	}
	httpSrv := &http.Server{Handler: c.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	collectorAddr := "http://" + listener.Addr().String()

	files, err := discovery.Discover(cfg.Dir, cfg.Discovery.Paths)
	if err != nil {
		return nil, fmt.Errorf("discover resources: %w", err)
	}

	entry, err := discovery.Bundle(cfg.Dir, files)
	if err != nil {
		return nil, fmt.Errorf("bundle discovery entrypoint: %w", err)
	}

	nodeCmd := exec.CommandContext(ctx, "node", entry)
	nodeCmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER="+collectorAddr)
	nodeCmd.Stdout = stdout
	nodeCmd.Stderr = stderr
	if err := nodeCmd.Run(); err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}

	return c.Snapshot(), nil
}
