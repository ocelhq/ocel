package dev

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/constants"
)

type host struct {
	srv     *devserver.Server
	address string
	close   func()
}

func startHost(ctx context.Context, opts Options, source valueSource) (*host, error) {
	cfg := opts.Config
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start dev server: %w", err)
	}
	address := listener.Addr().String()

	resources := devresources.New(devresources.ProjectName(cfg.Dir), devresources.Options{
		Open:       opts.OpenDocker,
		StateDir:   stateDir(cfg),
		AppOrigins: appOrigins(cfg.Dir, source),
		Stdout:     opts.Stdout,
	})

	srv := devserver.New("http://"+address, resources)
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)

	return &host{srv: srv, address: address, close: func() {
		_ = httpSrv.Close()
		stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), devresources.StopsWithin)
		defer cancel()
		if err := resources.Close(stopping); err != nil {
			fmt.Fprintln(opts.Stderr, "stop dev resources:", err)
		}
	}}, nil
}

func stateDir(cfg *projectconfig.Config) string {
	return filepath.Join(cfg.Dir, constants.ProjectStateDirName, "devresources")
}

func appOrigins(dir string, source valueSource) func() []string {
	return func() []string {
		var named string
		if values, err := source.read(dir); err == nil {
			named = values.merged()[portEnv]
		}
		port := cmp.Or(named, os.Getenv(portEnv), defaultPort)
		return []string{"http://localhost:" + port, "http://127.0.0.1:" + port}
	}
}
