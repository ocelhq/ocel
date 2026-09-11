package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/cli/link"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/election"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/pkg/constants"
)

var runLocal bool

var runCmd = &cobra.Command{
	Use:   "run -- <command> [args...]",
	Short: "Run a one-off command with your project's resource connections",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runRun(ctx, newDeps(), runLocal, cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	runCmd.Flags().BoolVarP(&runLocal, "local", "L", false, "Run with no console: .env alone carries every value and every resource")
}

func runRun(ctx context.Context, deps cmddeps.Deps, local bool, cwd string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	// TODO: unlike build/deploy, this never calls runtrace.Start, so discovery
	// below produces no spans or logs and nothing else says so.
	var creds credentials.Credentials
	if !local {
		loaded, err := deps.LoadCredentials()
		if err != nil {
			fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first, or `ocel run --local` to run without one.")
			return &exitsig.ExitError{Code: 1}
		}
		creds = loaded
	}

	cfg, err := projectconfig.ResolveOptional(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	leaderAddr, found, err := runningDevServer(cfg.Dir)
	if err != nil {
		return err
	}
	if found {
		return runOnceAsFollower(ctx, deps, leaderAddr, appArgs, stdout, stderr, stdin)
	}

	var consoleLink *devConsole
	if !local {
		apiURL := console.EffectiveBaseURL(creds.APIURL)
		bound, bindErr := link.Ensure(ctx, deps, cfg.Dir, apiURL, stdout, stderr, stdin)
		if bindErr != nil {
			return bindErr
		}
		consoleLink = &devConsole{apiURL: apiURL, token: creds.AccessToken, projectID: bound.ProjectID}
	}
	return runStandalone(ctx, deps, consoleLink, cfg, appArgs, stdout, stderr, stdin)
}

func runningDevServer(root string) (string, bool, error) {
	result, err := election.Elect(root)
	if err != nil {
		return "", false, fmt.Errorf("look for a running dev server: %w", err)
	}
	return result.LeaderAddr, result.Role == election.Follower, nil
}

func runOnceAsFollower(ctx context.Context, deps cmddeps.Deps, leaderAddr string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	stream, err := subscribeEnv(ctx, leaderAddr)
	if err != nil {
		return fmt.Errorf("connect to leader: %w", err)
	}
	defer stream.close()

	env, err := stream.next()
	if err != nil {
		return fmt.Errorf("connect to leader: %w", err)
	}

	return runChildOnce(ctx, deps, appArgs, env, stdin, stdout, stderr)
}

func runStandalone(ctx context.Context, deps cmddeps.Deps, link *devConsole, cfg *projectconfig.Config, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	file, err := dotenv.Load(cfg.Dir)
	if err != nil {
		return err
	}
	reportUnreadableLines(stdout, file.Unreadable)
	reportDotfile(stdout, cfg.Dir, file.Values, dotfileReadOnceAdvice)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start dev server: %w", err)
	}

	devServerAddr := "http://" + listener.Addr().String()

	var srv *devserver.Server
	var projectCfg resolve.Account
	if link == nil {
		reportLocal(stdout)
		srv = devserver.NewLocal(devServerAddr, filepath.Join(cfg.Dir, constants.ProjectStateDirName, "blob"))
	} else {
		projectCfg = resolveAccount(ctx, deps, link.apiURL, link.token, link.projectID, stderr)
		srv = devserver.New(link.apiURL, link.token, link.projectID, devServerAddr)
		srv.UseAccount(projectCfg)
	}
	srv.UseValues(storeValues(projectCfg.EnvVars, file.Values), envwire.Scope(cfg, false, ""))
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	resolved, err := discoverAndSync(ctx, srv, cfg, file.Values, invocation{name: "run", local: link == nil}, stdout, stderr)
	if err != nil {
		return err
	}

	return runChildOnce(ctx, deps, appArgs, resolved, stdin, stdout, stderr)
}

func runChildOnce(ctx context.Context, deps cmddeps.Deps, appArgs []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) error {
	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), env)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	child, err := spawnAppChild(ctx, appCmd, stdin, deps.StdinIsTerminal(stdin))
	if err != nil {
		return err
	}
	return appExitError(ctx, child.wait())
}
