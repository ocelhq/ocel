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
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/appbuilder"
	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/election"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/provision"
	"github.com/ocelhq/ocel/cli/internal/watcher"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
)

var watchDebounce = 300 * time.Millisecond

var devCmd = &cobra.Command{
	Use:   "dev -- <command> [args...]",
	Short: "Run your project in development mode",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		return runDev(cmd.Context(), defaultDeps(), cmd, cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func runDev(ctx context.Context, d deps, cmd *cobra.Command, cwd string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	// TODO: unlike build/deploy, this never calls obs.Start, so discovery
	// below produces no spans or logs and nothing else says so.
	creds, err := d.loadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	cfg, err := projectconfig.ResolveOptional(ctx, cwd)
	if err != nil {
		return err
	}

	apiURL := effectiveAPIURL(cmd, creds.APIURL)

	for range 3 {
		role, err := election.Elect(cfg.Dir)
		if err != nil {
			return fmt.Errorf("determine leader/follower role: %w", err)
		}

		if role.Role == election.Follower {
			return runFollower(ctx, role.LeaderAddr, appArgs, stdout, stderr, stdin)
		}

		link, err := ensureLinked(ctx, d, cfg.Dir, apiURL, stdout, stderr, stdin)
		if err != nil {
			return err
		}
		if err := runLeader(ctx, d, role, creds, apiURL, link.ProjectID, cfg, appArgs, stdout, stderr, stdin); !errors.Is(err, election.ErrLost) {
			return err
		}
	}
	return errors.New("determine leader/follower role: repeatedly lost the leader election; try again")
}

func runLeader(ctx context.Context, d deps, role election.Result, creds credentials.Credentials, apiURL, projectID string, cfg *projectconfig.Config, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	file, err := dotenv.Load(cfg.Dir)
	if err != nil {
		return err
	}
	reportDotfile(stdout, cfg.Dir, file.Values, dotfileWatchedAdvice)

	projectCfg := resolveProjectConfig(ctx, d, apiURL, creds.AccessToken, projectID, stderr)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start dev server: %w", err)
	}

	addr := listener.Addr().String()
	devServerAddr := "http://" + addr

	srv := devserver.New(apiURL, creds.AccessToken, projectID, devServerAddr)
	srv.UseProjectConfig(projectCfg)
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	go srv.RunDetector(ctx, func(err error) {
		fmt.Fprintln(stderr, "upload detection:", err)
	})

	if err := role.Claim(addr); err != nil {
		return err
	}
	defer role.Release()

	resolved, err := resolveOnce(ctx, srv, cfg, projectCfg.EnvVars, stdout, stderr)
	if err != nil {
		return err
	}
	srv.PushEnv(resolved)

	if err := watchAndReResolve(ctx, srv, cfg, projectCfg.EnvVars, stdout, stderr); err != nil {
		return fmt.Errorf("watch discovery paths: %w", err)
	}

	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), resolved)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	setNewProcessGroup(appCmd)
	appCmd.Cancel = func() error { return killProcessGroup(appCmd) }
	return waitExitError(appCmd.Run())
}

func resolveOnce(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, projectEnv map[string]string, stdout, stderr io.Writer) (map[string]string, error) {
	file, err := dotenv.Load(cfg.Dir)
	if err != nil {
		return nil, err
	}
	reportUnreadableLines(stdout, file.Unreadable)
	srv.UseValues(storeValues(projectEnv, file.Values), envScope(cfg, false, ""))
	return discoverAndSync(ctx, srv, cfg, file.Values, stdout, stderr)
}

func discoverAndSync(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, dotfile map[string]string, stdout, stderr io.Writer) (map[string]string, error) {
	if err := srv.Discover(ctx, cfg, stdout, stderr); err != nil {
		return nil, err
	}

	if err := srv.CheckEnv(ctx); err != nil {
		return nil, devRefusal(err, dotfileKeySet(dotfile))
	}

	appFolder := appbuilder.AppFolder(cfg.Apps)
	if err := checkStatableBinding(cfg.Apps, appFolder, srv.ScopedFolders()); err != nil {
		return nil, err
	}

	clientKeys := srv.ClientKeys()
	if err := generateClientAccessors(cfg, clientKeys); err != nil {
		return nil, err
	}

	syncResult := <-srv.Sync()
	if syncResult.Err != nil {
		return nil, fmt.Errorf("sync failed: %w", syncResult.Err)
	}

	reportLiveValues(stdout, syncResult.LiveKeys)
	return resolvedEnv(syncResult.ProjectConfig.EnvVars, syncResult.LiveValues, dotfile, syncResult.Resources, appFolder), nil
}

func reportLiveValues(stdout io.Writer, liveKeys []string) {
	if len(liveKeys) == 0 {
		return
	}
	keys := slices.Clone(liveKeys)
	slices.Sort(keys)
	fmt.Fprintf(stdout, "resolved %s once, at startup. Deployed, a rotated value is picked up within a bounded window; here, restart `ocel dev` to pick one up.\n", strings.Join(keys, ", "))
}

func watchAndReResolve(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, projectEnv map[string]string, stdout, stderr io.Writer) error {
	dirs, err := discovery.Dirs(cfg.Dir, cfg.Discovery.Paths)
	if err != nil {
		return fmt.Errorf("resolve watch directories: %w", err)
	}

	set := watcher.Set{Dirs: dirs, Files: []string{filepath.Join(cfg.Dir, dotenv.FileName)}}

	return watcher.Watch(ctx, set, watchDebounce, func() {
		srv.ResetManifest()
		resolved, err := resolveOnce(ctx, srv, cfg, projectEnv, stdout, stderr)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(stderr, "re-resolve failed:", err)
			}
			return
		}
		srv.PushEnv(resolved)
	}, func(err error) {
		fmt.Fprintln(stderr, "watch error:", err)
	})
}

func runFollower(ctx context.Context, leaderAddr string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	client := devv1connect.NewDevServiceClient(http.DefaultClient, "http://"+leaderAddr)

	stream, err := client.Subscribe(ctx, &devv1.SubscribeRequest{})
	if err != nil {
		return fmt.Errorf("connect to leader: %w", err)
	}
	defer stream.Close()

	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return fmt.Errorf("connect to leader: %w", err)
		}
		return errors.New("connect to leader: stream closed before first env push")
	}

	child, err := startFollowerChild(ctx, appArgs, stream.Msg().Env, stdin, stdout, stderr)
	if err != nil {
		return err
	}

	updates := make(chan map[string]string)
	streamDone := make(chan error, 1)
	go func() {
		for stream.Receive() {
			select {
			case updates <- stream.Msg().Env:
			case <-ctx.Done():
				return
			}
		}
		streamDone <- stream.Err()
	}()

	for {
		select {
		case err := <-child.done:
			if ctx.Err() != nil {
				return nil
			}
			return waitExitError(err)
		case env := <-updates:
			_ = killProcessGroup(child.cmd)
			<-child.done
			child, err = startFollowerChild(ctx, appArgs, env, stdin, stdout, stderr)
			if err != nil {
				return err
			}
		case <-streamDone:
			_ = killProcessGroup(child.cmd)
			<-child.done
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintln(stderr, "Leader disconnected. Restart `ocel dev` in the leader's terminal, then re-run this command.")
			return &ExitError{Code: 1}
		}
	}
}

type followerChild struct {
	cmd  *exec.Cmd
	done chan error
}

func startFollowerChild(ctx context.Context, appArgs []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (*followerChild, error) {
	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), env)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	setNewProcessGroup(appCmd)
	appCmd.Cancel = func() error { return killProcessGroup(appCmd) }
	if err := appCmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() { done <- appCmd.Wait() }()
	return &followerChild{cmd: appCmd, done: done}, nil
}

func waitExitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &ExitError{Code: exitErr.ExitCode()}
	}
	return err
}

func mergeEnv(base []string, projectEnv, liveValues, dotfile map[string]string, resources []provision.Resource, appFolder string) []string {
	return applyEnv(base, resolvedEnv(projectEnv, liveValues, dotfile, resources, appFolder))
}

func resolvedEnv(projectEnv, liveValues, dotfile map[string]string, resources []provision.Resource, appFolder string) map[string]string {
	merged := make(map[string]string, len(projectEnv)+len(liveValues)+len(dotfile)+1)
	for k, v := range projectEnv {
		merged[k] = v
	}
	for k, v := range liveValues {
		merged[k] = v
	}
	for k, v := range dotfile {
		merged[k] = v
	}
	for _, r := range resources {
		for k, v := range r.Env {
			merged[k] = v
		}
	}
	merged[appFolderEnv] = appFolder
	return merged
}

const appFolderEnv = "OCEL_APP_FOLDER"

func applyEnv(base []string, overrides map[string]string) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	for _, kv := range base {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			merged[kv[:i]] = kv[i+1:]
		}
	}
	for k, v := range overrides {
		merged[k] = v
	}

	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}
