package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/internal/credentials"
	"github.com/ocelhq/ocel/internal/devserver"
	"github.com/ocelhq/ocel/internal/discovery"
	"github.com/ocelhq/ocel/internal/localharness"
	"github.com/ocelhq/ocel/internal/projectconfig"
	"github.com/ocelhq/ocel/internal/provision"
)

// loadCredentials is a seam over credentials.Load so tests can simulate an
// unauthenticated CLI without touching the real keyring/credentials file.
var loadCredentials = credentials.Load

// startLocalHarness is a seam over localharness.Spawn so tests can back the
// hidden --local-harness flag with an httptest server instead of a real bun
// process. It returns the harness's host:port and a stop function.
var startLocalHarness = func(ctx context.Context, projectDir string) (string, func(), error) {
	proc, err := localharness.Spawn(ctx, localharness.SpawnConfig{
		Command: "bun",
		Args:    []string{"scripts/local-api-server.ts"},
		Env:     os.Environ(),
		Dir:     projectDir,
	})
	if err != nil {
		return "", nil, err
	}
	return proc.Addr, proc.Stop, nil
}

// devCmd runs the current Ocel project in development mode.
var devCmd = &cobra.Command{
	Use:   "dev -- <command> [args...]",
	Short: "Run your project in development mode",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		useLocalHarness, err := cmd.Flags().GetBool("local-harness")
		if err != nil {
			return err
		}
		return runDev(cmd.Context(), cwd, args, useLocalHarness, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	// Undocumented dogfooding escape hatch: routes provisioning through a
	// locally spawned harness (apps/web/scripts/local-api-server.ts) instead
	// of the control-plane API, which can't be up yet when it is itself the
	// app being started (see ocelhq-z7j).
	devCmd.Flags().Bool("local-harness", false, "route provisioning through a locally spawned harness")
	_ = devCmd.Flags().MarkHidden("local-harness")
}

// runDev resolves the project config, verifies auth, discovers and syncs
// resources, and spawns appArgs verbatim with the resolved environment. It
// does not start appArgs if auth, discovery, or sync fail.
//
// With useLocalHarness set, provisioning routes through a locally spawned
// harness process that lives only for the duration of the sync handshake:
// it is torn down before appArgs starts.
func runDev(ctx context.Context, cwd string, appArgs []string, useLocalHarness bool, stdout, stderr io.Writer, stdin io.Reader) error {
	creds, err := loadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	var srvOpts []devserver.Option
	stopHarness := func() {}
	if useLocalHarness {
		addr, stop, err := startLocalHarness(ctx, cfg.Dir)
		if err != nil {
			return fmt.Errorf("start local harness: %w", err)
		}
		var once sync.Once
		stopHarness = func() { once.Do(stop) }
		defer stopHarness()

		client := localharness.NewClient("http://" + addr + "/dev")
		srvOpts = append(srvOpts, devserver.WithProvisioner(client.FetchProjectConfig, client.Provision))
	}

	srv := devserver.New(creds.APIURL, creds.AccessToken, cfg.ProjectID, srvOpts...)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start dev server: %w", err)
	}
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	devServerAddr := "http://" + listener.Addr().String()

	files, err := discovery.Discover(cfg.Dir, cfg.Discovery.Paths)
	if err != nil {
		return fmt.Errorf("discover resources: %w", err)
	}

	entry, err := discovery.Bundle(cfg.Dir, files)
	if err != nil {
		return fmt.Errorf("bundle discovery entrypoint: %w", err)
	}

	nodeCmd := exec.CommandContext(ctx, "node", entry)
	nodeCmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER="+devServerAddr)
	nodeCmd.Stdout = stdout
	nodeCmd.Stderr = stderr
	if err := nodeCmd.Run(); err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}

	result := <-srv.Sync()
	stopHarness()
	if result.Err != nil {
		return fmt.Errorf("sync failed: %w", result.Err)
	}

	env := mergeEnv(os.Environ(), result.ProjectConfig.EnvVars, result.Resources)

	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = env
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	if err := appCmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &ExitError{Code: exitErr.ExitCode()}
		}
		return err
	}

	return nil
}

// mergeEnv merges, in increasing precedence: base (typically the CLI's
// inherited environment) < projectEnv (project-level env vars) < each
// resource's resolved Env entries.
func mergeEnv(base []string, projectEnv map[string]string, resources []provision.ProvisionedResource) []string {
	merged := make(map[string]string, len(base)+len(projectEnv))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			merged[kv[:i]] = kv[i+1:]
		}
	}
	for k, v := range projectEnv {
		merged[k] = v
	}
	for _, r := range resources {
		for k, v := range r.Env {
			merged[k] = v
		}
	}

	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}
