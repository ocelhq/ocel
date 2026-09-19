package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/devlock"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/election"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
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

		ctx, stop := installDevInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runRun(ctx, newDeps(), cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func runRun(ctx context.Context, deps cmddeps.Deps, cwd string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	// TODO: unlike build/deploy, this never calls runtrace.Start, so discovery
	// below produces no spans or logs and nothing else says so.
	cfg, err := projectconfig.ResolveOptional(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	leader, found, err := runningDevServer(cfg.Dir)
	if err != nil {
		return err
	}
	if found {
		return runOnceAsFollower(ctx, deps, leader, appArgs, stdout, stderr, stdin)
	}

	return runStandalone(ctx, deps, cfg, targetScope(cfg, cwd), appArgs, stdout, stderr, stdin)
}

func runningDevServer(root string) (devlock.Lease, bool, error) {
	result, err := election.Elect(root)
	if err != nil {
		return devlock.Lease{}, false, fmt.Errorf("look for a running dev server: %w", err)
	}
	return result.Leader, result.Role == election.Follower, nil
}

func runOnceAsFollower(ctx context.Context, deps cmddeps.Deps, leader devlock.Lease, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	stream, err := subscribeEnv(ctx, leader)
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

func runStandalone(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, scope envgate.Scope, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	file, err := dotenv.Load(cfg.Dir)
	if err != nil {
		return err
	}
	reportUnreadableLines(stdout, file.Unreadable)
	reportDotfile(stdout, cfg.Dir, file.Values, dotfileReadOnceAdvice)

	host, err := startDevHost(ctx, deps, cfg, stdout, stderr)
	if err != nil {
		return err
	}
	defer host.close()
	srv, shared := host.srv, host.shared
	srv.UseValues(storeValues(shared.values, file.Values), envwire.Scope(cfg, false, ""))

	resolved, err := discoverAndSync(ctx, srv, cfg, shared.values, file.Values, scope, invocation{name: "run", loggedOut: shared.loggedOut}, stdout, stderr)
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
