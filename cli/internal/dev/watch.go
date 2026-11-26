package dev

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
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

type watcher struct {
	*filewatch.Watcher
	paths []string
}

func (w *watcher) notice() string {
	return "Watching for changes in " + strings.Join(w.paths, ", ")
}

func watchAndResolve(ctx context.Context, server *devserver.Server, cfg *project.Project, local localCommand, session *run.Span, onResolved func(env map[string]string, changed []string), onFailed func(error)) (*watcher, error) {
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

	watching, err := filewatch.Start(ctx, filewatch.Config{Paths: paths, SkipDir: discovery.IsSkippedDir, Debounce: watchDebounce, OnChange: func(changed []string) {
		server.ResetDeclarations()
		changeCtx, change := session.Child(ctx, "", changeTitle)
		resolved, values, err := resolveOnce(changeCtx, server, cfg, local.afterChange(), change)
		if err != nil {
			change.End(err)
			if refusesStart(err) {
				server.PushNotReady()
			}
			onFailed(err)
			return
		}
		change.EndWithTitle("Re-resolved " + values.summary())
		server.PushEnv(resolved)
		onResolved(resolved, changed)
	}, OnError: func(err error) {
		session.Warn(fmt.Sprintf("Watching for changes failed: %v", err))
	}})
	if err != nil {
		return nil, err
	}
	names := slices.Clone(local.source.files())
	for _, root := range roots {
		name, err := filepath.Rel(cfg.Dir, root.Dir)
		if err != nil {
			name = root.Dir
		}
		name = filepath.ToSlash(name) + "/"
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return &watcher{Watcher: watching, paths: names}, nil
}
