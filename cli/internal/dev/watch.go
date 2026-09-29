package dev

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/filewatch"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
)

var (
	watchDebounce = 300 * time.Millisecond
	startWatching = watchAndResolve
)

func watchAndResolve(ctx context.Context, server *devserver.Server, cfg *project.Project, invoked invocation, session *run.Span, onResolved func(map[string]string)) (*filewatch.Watcher, error) {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	dirs, err := discovery.Dirs(roots)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	paths := filewatch.Paths{Dirs: dirs}
	for _, name := range invoked.source.files() {
		paths.Files = append(paths.Files, filepath.Join(cfg.Dir, name))
	}

	return filewatch.Start(ctx, filewatch.Config{Paths: paths, Debounce: watchDebounce, OnChange: func() {
		server.ResetDeclarations()
		change := session.Child("", changeTitle)
		resolved, err := resolveOnce(ctx, server, cfg, invoked, change)
		change.End(err)
		if err != nil {
			return
		}
		server.PushEnv(resolved)
		onResolved(resolved)
	}, OnError: func(err error) {
		session.Warn(fmt.Sprintf("Watching for changes failed: %v", err))
	}})
}
