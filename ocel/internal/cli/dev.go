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

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/internal/credentials"
	"github.com/ocelhq/ocel/internal/devserver"
	"github.com/ocelhq/ocel/internal/discovery"
	"github.com/ocelhq/ocel/internal/election"
	"github.com/ocelhq/ocel/internal/lockfile"
	"github.com/ocelhq/ocel/internal/projectconfig"
	"github.com/ocelhq/ocel/internal/provision"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
)

// loadCredentials is a seam over credentials.Load so tests can simulate an
// unauthenticated CLI without touching the real keyring/credentials file.
var loadCredentials = credentials.Load

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
		return runDev(cmd.Context(), cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

// runDev resolves the project config, verifies auth, then elects this
// process as either the leader (first `ocel dev` for the project) or a
// follower (a later one), and runs the corresponding flow.
func runDev(ctx context.Context, cwd string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	creds, err := loadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

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
		if err := runLeader(ctx, creds, cfg, appArgs, stdout, stderr, stdin); !errors.Is(err, errLostElection) {
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
func runLeader(ctx context.Context, creds credentials.Credentials, cfg *projectconfig.Config, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	srv := devserver.New(creds.APIURL, creds.AccessToken, cfg.ProjectID)

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

	files, err := discovery.Discover(cfg.Dir, cfg.Discovery.Paths)
	if err != nil {
		return fmt.Errorf("discover resources: %w", err)
	}

	entry, err := discovery.Bundle(cfg.Dir, files)
	if err != nil {
		return fmt.Errorf("bundle discovery entrypoint: %w", err)
	}

	nodeCmd := exec.CommandContext(ctx, "node", entry)
	nodeCmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER=http://"+addr)
	nodeCmd.Stdout = stdout
	nodeCmd.Stderr = stderr
	if err := nodeCmd.Run(); err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}

	syncResult := <-srv.Sync()
	if syncResult.Err != nil {
		return fmt.Errorf("sync failed: %w", syncResult.Err)
	}

	resolved := resolvedEnv(syncResult.ProjectConfig.EnvVars, syncResult.Resources)
	srv.PushEnv(resolved)

	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), resolved)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	return waitExitError(appCmd.Run())
}

// runFollower connects to the leader at leaderAddr, waits for its initial
// env push, and spawns appArgs with it. If the leader disconnects while
// appArgs is running, the child is stopped and runFollower returns a
// non-zero *ExitError after printing a message instructing the user to
// restart the leader.
func runFollower(ctx context.Context, leaderAddr string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	client := devv1connect.NewDevServiceClient(http.DefaultClient, "http://"+leaderAddr)

	stream, err := client.Subscribe(ctx, &devv1.SubscribeRequest{})
	if err != nil {
		return fmt.Errorf("connect to leader: %w", err)
	}
	defer stream.Close()

	if !stream.Receive() {
		return fmt.Errorf("connect to leader: %w", stream.Err())
	}

	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), stream.Msg().Env)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	setNewProcessGroup(appCmd)
	if err := appCmd.Start(); err != nil {
		return err
	}

	childDone := make(chan error, 1)
	go func() { childDone <- appCmd.Wait() }()

	streamDone := make(chan error, 1)
	go func() {
		// Draining further pushes (rather than stopping after the first)
		// lets a stream close/error be distinguished from a leader
		// disconnect. Restarting the child on later updates is future work
		// (the file-watcher/re-resolve issue).
		for stream.Receive() {
		}
		streamDone <- stream.Err()
	}()

	select {
	case err := <-childDone:
		return waitExitError(err)
	case <-streamDone:
		_ = killProcessGroup(appCmd)
		err := <-childDone
		if ctx.Err() != nil {
			// The stream closed because we are shutting down, not because
			// the leader went away.
			return waitExitError(err)
		}
		fmt.Fprintln(stderr, "Leader disconnected. Restart `ocel dev` in the leader's terminal, then re-run this command.")
		return &ExitError{Code: 1}
	}
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
