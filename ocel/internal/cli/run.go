package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/internal/credentials"
	"github.com/ocelhq/ocel/internal/devserver"
	"github.com/ocelhq/ocel/internal/election"
	"github.com/ocelhq/ocel/internal/projectconfig"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
)

// runCmd runs a one-off command against the current Ocel project's resolved
// resource connections, without a persistent dev session.
var runCmd = &cobra.Command{
	Use:   "run -- <command> [args...]",
	Short: "Run a one-off command with your project's resource connections",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		return runRun(cmd.Context(), cmd, cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

// runRun resolves the project config, verifies auth, then either reuses a
// running leader's resolved env as a one-shot follower, or performs a
// standalone ephemeral resolution when no leader exists, before running
// appArgs once and returning with its exit code. Unlike `ocel dev`, it never
// watches for file changes and never writes a leader lockfile. cmd carries
// the root --api-url flag so an explicit override wins over the persisted
// credentials' API URL (see effectiveAPIURL); it may be nil in tests.
func runRun(ctx context.Context, cmd *cobra.Command, cwd string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	creds, err := loadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	cfg, err := projectconfig.Resolve(cwd)
	if err != nil {
		return err
	}

	role, err := election.Elect(cfg.ProjectID)
	if err != nil {
		return fmt.Errorf("determine leader/follower role: %w", err)
	}

	if role.Role == election.Follower {
		return runOnceAsFollower(ctx, role.LeaderAddr, appArgs, stdout, stderr, stdin)
	}
	return runStandalone(ctx, creds, effectiveAPIURL(cmd, creds.APIURL), cfg, appArgs, stdout, stderr, stdin)
}

// runOnceAsFollower connects to the leader at leaderAddr, takes the first
// env it pushes, and spawns appArgs with it. Unlike ocel dev's follower, it
// does not react to later pushes or leader disconnects: it runs the command
// once and returns as soon as it exits.
func runOnceAsFollower(ctx context.Context, leaderAddr string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	client := devv1connect.NewDevServiceClient(http.DefaultClient, "http://"+leaderAddr)

	stream, err := client.Subscribe(ctx, &devv1.SubscribeRequest{})
	if err != nil {
		return fmt.Errorf("connect to leader: %w", err)
	}
	defer stream.Close()

	if !stream.Receive() {
		return fmt.Errorf("connect to leader: %w", stream.Err())
	}

	return runChildOnce(ctx, appArgs, stream.Msg().Env, stdin, stdout, stderr)
}

// runStandalone spins an ephemeral in-process dev server on a random port
// for its own discovery + sync, injects the resolved env, runs appArgs once,
// and tears the server down before returning. It never writes a lockfile, so
// it never advertises itself as a leader to other ocel dev/run processes.
// apiURL is the resolved Ocel API origin provisioning is authenticated
// against (see effectiveAPIURL).
func runStandalone(ctx context.Context, creds credentials.Credentials, apiURL string, cfg *projectconfig.Config, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	srv := devserver.New(apiURL, creds.AccessToken, cfg.ProjectID)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start dev server: %w", err)
	}
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	devServerAddr := "http://" + listener.Addr().String()

	resolved, err := discoverAndSync(ctx, srv, cfg, devServerAddr, stdout, stderr)
	if err != nil {
		return err
	}

	return runChildOnce(ctx, appArgs, resolved, stdin, stdout, stderr)
}

// runChildOnce runs appArgs to completion with env applied over the
// inherited environment, translating its exit into an *ExitError.
func runChildOnce(ctx context.Context, appArgs []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) error {
	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), env)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	return waitExitError(appCmd.Run())
}
