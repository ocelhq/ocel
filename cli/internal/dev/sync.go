package dev

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func resolveOnce(ctx context.Context, srv *devserver.Server, cfg *project.Project, invoked invocation, span *run.Span) (map[string]string, error) {
	values, err := invoked.source.read(cfg.Dir)
	if err != nil {
		return nil, err
	}
	reportUnreadableLines(span, values)
	srv.UseValues(values.merged(), variablescope.ForDev(cfg))
	return discoverAndSync(ctx, srv, cfg, values, variablescope.ForDev(cfg), invoked, span)
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

func discoverAndSync(ctx context.Context, srv *devserver.Server, cfg *project.Project, values valueLayers, scope variables.Scope, invoked invocation, span *run.Span) (map[string]string, error) {
	if err := discover(ctx, srv, cfg, span); err != nil {
		return nil, refusedSync(srv, err)
	}

	if err := srv.CheckEnv(ctx); err != nil {
		return nil, describeRefusal(err, values.keys(), invoked)
	}

	appFolder := project.SharedFolder(cfg.Apps)
	if err := refuseUnstatableBinding(invoked.source, cfg.Apps, appFolder, filepath.Base(cfg.Path), srv.ScopedFolders()); err != nil {
		return nil, err
	}

	clientKeys, err := srv.ClientKeys()
	if err != nil {
		return nil, err
	}
	if _, err := clientenv.GenerateProjectAccessors(cfg, clientKeys); err != nil {
		return nil, err
	}

	result := <-srv.SyncResults()
	if result.Err != nil {
		return nil, fmt.Errorf("sync failed: %w", result.Err)
	}

	reportSecretValues(span, result.SecretKeys)
	return resolvedEnv(result.SecretValues, values.merged(), result.Resources, runtimeAccess{url: result.DevServerURL, token: result.AppToken}, appFolder, scope), nil
}

func discover(ctx context.Context, srv *devserver.Server, cfg *project.Project, span *run.Span) error {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return err
	}

	prepared, err := discovery.Prepare(cfg.Dir, roots)
	if err != nil {
		return err
	}

	_ = srv.TakeSDKRefusal()
	stdout, stderr := span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDOUT), span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDERR)
	err = discovery.Run(run.ContextWithSpan(ctx, span), cfg.Dir, prepared, srv.DiscoveryTarget(), stdout, stderr)
	if refused := srv.TakeSDKRefusal(); refused != nil {
		return refused
	}
	return err
}

func refusedSync(srv *devserver.Server, err error) error {
	select {
	case result := <-srv.SyncResults():
		if result.Err != nil {
			return result.Err
		}
	default:
	}
	return err
}
