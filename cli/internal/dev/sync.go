package dev

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func resolveOnce(ctx context.Context, server *devserver.Server, cfg *project.Project, invoked invocation, span *run.Span) (map[string]string, error) {
	values, err := invoked.source.read(cfg.Dir)
	if err != nil {
		return nil, err
	}
	reportUnreadableLines(span, values)
	server.UseValues(values.merged(), variablescope.ForDev(cfg))
	return discoverAndSync(ctx, server, cfg, values, variablescope.ForDev(cfg), invoked, span)
}

func targetScope(cfg *project.Project, cwd string) variables.Scope {
	scope := variablescope.ForDev(cfg)
	target, deepest := -1, -1
	for i, app := range cfg.Apps {
		rel, err := filepath.Rel(filepath.Join(cfg.Dir, app.Path), cwd)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if depth := len(filepath.Clean(app.Path)); depth > deepest {
			target, deepest = i, depth
		}
	}
	if target >= 0 {
		scope.Apps = []variables.App{scope.Apps[target]}
	}
	return scope
}

func discoverAndSync(ctx context.Context, server *devserver.Server, cfg *project.Project, values valueLayers, scope variables.Scope, invoked invocation, span *run.Span) (map[string]string, error) {
	if err := discover(ctx, server, cfg, span); err != nil {
		return nil, refusedSync(server, err)
	}

	if err := server.RefuseIncompleteEnv(ctx); err != nil {
		return nil, describeRefusal(err, values.keys(), invoked)
	}

	appFolder := project.SharedFolder(cfg.Apps)
	if err := refuseUnstatableBinding(invoked.source, cfg.Apps, appFolder, filepath.Base(cfg.Path), server.ScopedFolders()); err != nil {
		return nil, err
	}

	result := <-server.SyncResults()
	if result.Err != nil {
		return nil, fmt.Errorf("sync failed: %w", result.Err)
	}

	reportSecretValues(span, result.SecretKeys)
	env := resolvedEnv(result.SecretValues, values.merged(), result.Resources, runtimeAccess{url: result.DevServerURL, token: result.AppToken}, appFolder, scope)
	nextEnv, err := nextEnvOf(cfg, scope, server.PublicKeys())
	if err != nil {
		return nil, err
	}
	maps.Copy(env, nextEnv)
	return env, nil
}

func nextEnvOf(cfg *project.Project, scope variables.Scope, declared []string) (map[string]string, error) {
	if !slices.ContainsFunc(scope.Apps, func(app variables.App) bool { return app.Framework == buildoutput.FrameworkNext }) {
		return nil, nil
	}
	if err := node.Ensure(cfg.Dir); err != nil {
		return nil, err
	}
	return nextAppEnv(scope, node.NextAdapterPath(cfg.Dir), declared), nil
}

func discover(ctx context.Context, server *devserver.Server, cfg *project.Project, span *run.Span) error {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return err
	}

	prepared, err := discovery.Prepare(cfg.Dir, roots)
	if err != nil {
		return err
	}

	_ = server.TakeSDKRefusal()
	stdout, stderr := span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDOUT), span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDERR)
	err = discovery.Run(run.ContextWithSpan(ctx, span), cfg.Dir, prepared, server.DiscoveryTarget(), stdout, stderr)
	if refused := server.TakeSDKRefusal(); refused != nil {
		return refused
	}
	return err
}

func refusedSync(server *devserver.Server, err error) error {
	select {
	case result := <-server.SyncResults():
		if result.Err != nil {
			return result.Err
		}
	default:
	}
	return err
}
