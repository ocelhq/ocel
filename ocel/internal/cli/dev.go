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
	"time"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/internal/credentials"
	"github.com/ocelhq/ocel/internal/devserver"
	"github.com/ocelhq/ocel/internal/discovery"
	"github.com/ocelhq/ocel/internal/election"
	"github.com/ocelhq/ocel/internal/lockfile"
	"github.com/ocelhq/ocel/internal/projectconfig"
	"github.com/ocelhq/ocel/internal/provision"
	"github.com/ocelhq/ocel/internal/watcher"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
)

// loadCredentials is a seam over credentials.Load so tests can simulate an
// unauthenticated CLI without touching the real keyring/credentials file.
var loadCredentials = credentials.Load

// watchDebounce is the quiet period the leader's file watcher waits for
// after the last change under discovery.paths before re-resolving. It's a
// var so tests can shorten it.
var watchDebounce = 300 * time.Millisecond

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
		return runDev(cmd.Context(), cmd, cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

// runDev resolves the project config, verifies auth, discovers and syncs
// resources, and spawns appArgs verbatim with the resolved environment. It
// does not start appArgs if auth, discovery, or sync fail. cmd carries the
// root --api-url flag so an explicit override wins over the persisted
// credentials' API URL (see effectiveAPIURL); it may be nil in tests.
func runDev(ctx context.Context, cmd *cobra.Command, cwd string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	creds, err := loadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	apiURL := effectiveAPIURL(cmd, creds.APIURL)

	// A concurrent `ocel dev` can create the lockfile between our Elect and
	// runLeader's exclusive lockfile.Create; on that loss, re-elect to join
	// the winner as a follower.
	for range 3 {
		role, err := election.Elect(cfg.ProjectID)
		if err != nil {
			return fmt.Errorf("determine leader/follower role: %w", err)
		}

		if role.Role == election.Follower {
			return runFollower(ctx, role.LeaderAddr, appArgs, stdout, stderr, stdin)
		}
		if err := runLeader(ctx, creds, apiURL, cfg, appArgs, stdout, stderr, stdin); !errors.Is(err, errLostElection) {
			return err
		}
	}
	return errors.New("determine leader/follower role: repeatedly lost the leader election; try again")
}

// errLostElection reports that another process created the leader lockfile
// between this process's Elect and its own lockfile.Create.
var errLostElection = errors.New("another process became leader first")

// runLeader discovers and syncs resources, pushes the resolved env to any
// connected followers, and spawns appArgs verbatim with that same
// environment. It does not start appArgs if auth, discovery, or sync fail.
// apiURL is the resolved Ocel API origin provisioning is authenticated
// against (see effectiveAPIURL).
func runLeader(ctx context.Context, creds credentials.Credentials, apiURL string, cfg *projectconfig.Config, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	srv := devserver.New(apiURL, creds.AccessToken, cfg.ProjectID)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start dev server: %w", err)
	}
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	addr := listener.Addr().String()
	if err := lockfile.Create(cfg.ProjectID, addr); err != nil {
		if errors.Is(err, os.ErrExist) {
			return errLostElection
		}
		return fmt.Errorf("write leader lockfile: %w", err)
	}
	defer lockfile.Remove(cfg.ProjectID)

	devServerAddr := "http://" + addr

	resolved, err := discoverAndSync(ctx, srv, cfg, devServerAddr, stdout, stderr)
	if err != nil {
		return err
	}
	srv.PushEnv(resolved)

	if err := watchAndReResolve(ctx, srv, cfg, devServerAddr, stdout, stderr); err != nil {
		return fmt.Errorf("watch discovery paths: %w", err)
	}

	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), resolved)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	return waitExitError(appCmd.Run())
}

// discoverAndSync runs discovery over cfg's resolved discovery.paths,
// bundles and executes the entrypoint against srv (which accumulates
// Declare calls into its manifest), waits for the resulting /sync
// provisioning, and returns the full resolved env — ready to push to
// followers or apply to the leader's own child.
func discoverAndSync(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, devServerAddr string, stdout, stderr io.Writer) (map[string]string, error) {
	files, err := discovery.Discover(cfg.Dir, cfg.Discovery.Paths)
	if err != nil {
		return nil, fmt.Errorf("discover resources: %w", err)
	}

	entry, err := discovery.Bundle(cfg.Dir, files)
	if err != nil {
		return nil, fmt.Errorf("bundle discovery entrypoint: %w", err)
	}

	nodeCmd := exec.CommandContext(ctx, "node", entry)
	nodeCmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER="+devServerAddr)
	nodeCmd.Stdout = stdout
	nodeCmd.Stderr = stderr
	if err := nodeCmd.Run(); err != nil {
		return nil, fmt.Errorf("discovery failed: %w", err)
	}

	syncResult := <-srv.Sync()
	if syncResult.Err != nil {
		return nil, fmt.Errorf("sync failed: %w", syncResult.Err)
	}

	return resolvedEnv(syncResult.ProjectConfig.EnvVars, syncResult.Resources), nil
}

// watchAndReResolve watches cfg's resolved discovery.paths and, on a
// debounced change, resets the manifest and re-runs discoverAndSync (a full
// re-discovery: declares from the new run replace the prior manifest), then
// pushes the freshly resolved env to every connected follower. It returns
// once the watch is established; re-resolution happens in the background
// until ctx is done.
func watchAndReResolve(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, devServerAddr string, stdout, stderr io.Writer) error {
	dirs, err := discovery.Dirs(cfg.Dir, cfg.Discovery.Paths)
	if err != nil {
		return fmt.Errorf("resolve watch directories: %w", err)
	}

	return watcher.Watch(ctx, dirs, watchDebounce, func() {
		srv.ResetManifest()
		resolved, err := discoverAndSync(ctx, srv, cfg, devServerAddr, stdout, stderr)
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

// runFollower connects to the leader at leaderAddr, waits for its initial
// env push, and spawns appArgs with it. On every later push (the leader
// re-resolved after a watched file changed), the child is stopped and
// restarted with the new env. If the leader disconnects, the child is
// stopped and runFollower returns a non-zero *ExitError after printing a
// message instructing the user to restart the leader.
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
			// A cancelled ctx (e.g. Ctrl+C) kills the child too; don't
			// report its signal death as an app failure.
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
			// A cancelled ctx (e.g. Ctrl+C) also ends the stream; only a
			// disconnect the user didn't initiate warrants the message.
			if ctx.Err() != nil {
				return nil
			}
			fmt.Fprintln(stderr, "Leader disconnected. Restart `ocel dev` in the leader's terminal, then re-run this command.")
			return &ExitError{Code: 1}
		}
	}
}

// followerChild is a running app child process along with the channel its
// exit is delivered on.
type followerChild struct {
	cmd  *exec.Cmd
	done chan error
}

// startFollowerChild spawns appArgs with env applied over the inherited
// environment, in its own process group so a later restart or leader
// disconnect can kill it (and anything it forked) as a unit.
func startFollowerChild(ctx context.Context, appArgs []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (*followerChild, error) {
	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), env)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	setNewProcessGroup(appCmd)
	if err := appCmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() { done <- appCmd.Wait() }()
	return &followerChild{cmd: appCmd, done: done}, nil
}

// waitExitError converts the error from an *exec.Cmd's Run/Wait into an
// *ExitError carrying the child's real exit code, when possible.
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

// mergeEnv merges, in increasing precedence: base (typically the CLI's
// inherited environment) < projectEnv (project-level env vars) < each
// resource's resolved Env entries.
func mergeEnv(base []string, projectEnv map[string]string, resources []provision.ProvisionedResource) []string {
	return applyEnv(base, resolvedEnv(projectEnv, resources))
}

// resolvedEnv flattens projectEnv and every resource's Env entries into a
// single map, in the same precedence used by mergeEnv (project vars <
// resource vars). This is the map both the leader's own child uses and
// what's pushed to followers verbatim.
func resolvedEnv(projectEnv map[string]string, resources []provision.ProvisionedResource) map[string]string {
	merged := make(map[string]string, len(projectEnv))
	for k, v := range projectEnv {
		merged[k] = v
	}
	for _, r := range resources {
		for k, v := range r.Env {
			merged[k] = v
		}
	}
	return merged
}

// applyEnv overlays overrides onto base (a "KEY=VALUE" slice, typically
// os.Environ()), returning the result in the same "KEY=VALUE" form.
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
