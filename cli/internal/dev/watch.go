package dev

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/filewatch"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

var (
	watchDebounce = 300 * time.Millisecond
	startWatching = watchAndResolve
)

func watchAndResolve(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, run invocation, stdout, stderr io.Writer, onResolved func(map[string]string)) (*filewatch.Watcher, error) {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	dirs, err := discovery.Dirs(roots)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	paths := filewatch.Paths{Dirs: dirs}
	for _, name := range run.source.files() {
		paths.Files = append(paths.Files, filepath.Join(cfg.Dir, name))
	}

	return filewatch.Start(ctx, filewatch.Config{Paths: paths, Debounce: watchDebounce, OnChange: func() {
		srv.ResetDeclarations()
		resolved, err := resolveOnce(ctx, srv, cfg, run, stdout, stderr)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(stderr, "re-resolve failed:", err)
			}
			return
		}
		srv.PushEnv(resolved)
		onResolved(resolved)
	}, OnError: func(err error) {
		fmt.Fprintln(stderr, "watch error:", err)
	}})
}
