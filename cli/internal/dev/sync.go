package dev

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clientenv"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
)

func resolveOnce(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, run invocation, stdout, stderr io.Writer) (map[string]string, error) {
	values, err := run.source.read(cfg.Dir)
	if err != nil {
		return nil, err
	}
	reportUnreadableLines(stdout, values)
	srv.UseValues(values.merged(), variablescope.ForDev(cfg))
	return discoverAndSync(ctx, srv, cfg, values, variablescope.ForDev(cfg), run, stdout, stderr)
}

func targetScope(cfg *projectconfig.Config, cwd string) variables.Scope {
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

func discoverAndSync(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, values valueLayers, scope variables.Scope, run invocation, stdout, stderr io.Writer) (map[string]string, error) {
	if err := discover(ctx, srv, cfg, stdout, stderr); err != nil {
		return nil, refusedSync(srv, err)
	}

	if err := srv.CheckEnv(ctx); err != nil {
		return nil, describeRefusal(err, values.keys(), run)
	}

	appFolder := projectconfig.SharedFolder(cfg.Apps)
	if err := refuseUnstatableBinding(run.source, cfg.Apps, appFolder, filepath.Base(cfg.Path), srv.ScopedFolders()); err != nil {
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

	reportSecretValues(stdout, result.SecretKeys)
	return resolvedEnv(result.SecretValues, values.merged(), result.Resources, runtimeAccess{url: result.DevServerURL, token: result.AppToken}, appFolder, scope), nil
}

// TODO: unlike build/deploy, ocel dev and ocel run never call runtrace.Start, so
// discovery here produces no spans or logs and nothing else says so.
func discover(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, stdout, stderr io.Writer) error {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return err
	}

	prepared, err := discovery.Prepare(cfg.Dir, roots)
	if err != nil {
		return err
	}

	_ = srv.TakeSDKRefusal()
	err = discovery.Run(ctx, cfg.Dir, prepared, srv.DiscoveryTarget(), stdout, stderr)
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
