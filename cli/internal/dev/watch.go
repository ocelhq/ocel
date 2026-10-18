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

func watchAndResolve(ctx context.Context, server *devserver.Server, cfg *project.Project, local localCommand, session *run.Span, onResolved func(env map[string]string, changed []string), onFailed func(error)) (*filewatch.Watcher, error) {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	dirs, err := discovery.Dirs(roots)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	paths := filewatch.Paths{Dirs: dirs}
	for _, name := range local.source.files() {
		paths.Files = append(paths.Files, filepath.Join(cfg.Dir, name))
	}

	return filewatch.Start(ctx, filewatch.Config{Paths: paths, SkipDir: discovery.IsSkippedDir, Debounce: watchDebounce, OnChange: func(changed []string) {
		server.ResetDeclarations()
		change := session.Child("", changeTitle)
		resolved, err := resolveOnce(ctx, server, cfg, local, change)
		change.End(err)
		if err != nil {
			onFailed(err)
			return
		}
		server.PushEnv(resolved)
		onResolved(resolved, changed)
	}, OnError: func(err error) {
		session.Warn(fmt.Sprintf("Watching for changes failed: %v", err))
	}})
}
