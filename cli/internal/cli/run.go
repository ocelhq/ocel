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

	"github.com/ocelhq/ocel/cli/internal/credentials"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/election"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	devv1 "github.com/ocelhq/ocel/pkg/proto/dev/v1"
	"github.com/ocelhq/ocel/pkg/proto/dev/v1/devv1connect"
)

var runCmd = &cobra.Command{
	Use:   "run -- <command> [args...]",
	Short: "Run a one-off command with your project's resource connections",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		return runRun(cmd.Context(), defaultDeps(), cmd, cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func runRun(ctx context.Context, d deps, cmd *cobra.Command, cwd string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
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

	leaderAddr, found, err := runningDevServer(cfg.Dir)
	if err != nil {
		return err
	}
	if found {
		return runOnceAsFollower(ctx, leaderAddr, appArgs, stdout, stderr, stdin)
	}

	link, err := ensureLinked(ctx, d, cfg.Dir, apiURL, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	return runStandalone(ctx, d, creds, apiURL, link.ProjectID, cfg, appArgs, stdout, stderr, stdin)
}

func runningDevServer(root string) (string, bool, error) {
	result, err := election.Elect(root)
	if err != nil {
		return "", false, fmt.Errorf("look for a running dev server: %w", err)
	}
	return result.LeaderAddr, result.Role == election.Follower, nil
}

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

func runStandalone(ctx context.Context, d deps, creds credentials.Credentials, apiURL, projectID string, cfg *projectconfig.Config, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	file, err := dotenv.Load(cfg.Dir)
	if err != nil {
		return err
	}
	reportUnreadableLines(stdout, file.Unreadable)
	reportDotfile(stdout, cfg.Dir, file.Values, dotfileReadOnceAdvice)

	projectCfg := resolveProjectConfig(ctx, d, apiURL, creds.AccessToken, projectID, stderr)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start dev server: %w", err)
	}

	devServerAddr := "http://" + listener.Addr().String()

	srv := devserver.New(apiURL, creds.AccessToken, projectID, devServerAddr)
	srv.UseProjectConfig(projectCfg)
	srv.UseValues(storeValues(projectCfg.EnvVars, file.Values), envScope(cfg, false, ""))
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	resolved, err := discoverAndSync(ctx, srv, cfg, file.Values, stdout, stderr)
	if err != nil {
		return err
	}

	return runChildOnce(ctx, appArgs, resolved, stdin, stdout, stderr)
}

func runChildOnce(ctx context.Context, appArgs []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) error {
	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), env)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	setNewProcessGroup(appCmd)
	appCmd.Cancel = func() error { return killProcessGroup(appCmd) }
	return waitExitError(appCmd.Run())
}
