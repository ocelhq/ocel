package cli

import (
	"cmp"
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
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/console/credentials"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/election"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/watcher"
	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

var (
	watchDebounce = 300 * time.Millisecond
	startWatching = watchAndReResolve
)

var devLocal bool

var devCmd = &cobra.Command{
	Use:   "dev -- <command> [args...]",
	Short: "Run your project in development mode",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := installInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runDev(ctx, newDeps(), devLocal, cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	devCmd.Flags().BoolVarP(&devLocal, "local", "L", false, "Run with no console: .env alone carries every value and every resource")
}

type devConsole struct {
	apiURL    string
	token     string
	projectID string
}

func runDev(ctx context.Context, deps cmddeps.Deps, local bool, cwd string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	// TODO: unlike build/deploy, this never calls runtrace.Start, so discovery
	// below produces no spans or logs and nothing else says so.
	var creds credentials.Credentials
	if !local {
		loaded, err := deps.LoadCredentials()
		if err != nil {
			fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first, or `ocel dev --local` to run without one.")
			return &exitsig.ExitError{Code: 1}
		}
		creds = loaded
	}

	cfg, err := projectconfig.ResolveOptional(ctx, cwd, explicitConfigPath())
	if err != nil {
		return err
	}

	for range 3 {
		role, err := election.Elect(cfg.Dir)
		if err != nil {
			return fmt.Errorf("determine leader/follower role: %w", err)
		}

		if role.Role == election.Follower {
			return runFollower(ctx, deps, role.LeaderAddr, appArgs, stdout, stderr, stdin)
		}

		var link *devConsole
		if !local {
			apiURL := console.EffectiveBaseURL(creds.APIURL)
			bound, bindErr := ensureConsoleBinding(ctx, deps, cfg.Dir, apiURL, stdout, stderr, stdin)
			if bindErr != nil {
				return bindErr
			}
			link = &devConsole{apiURL: apiURL, token: creds.AccessToken, projectID: bound.ProjectID}
		}
		if err := runLeader(ctx, deps, role, link, cfg, appArgs, stdout, stderr, stdin); !errors.Is(err, election.ErrLost) {
			return err
		}
	}
	return errors.New("determine leader/follower role: repeatedly lost the leader election; try again")
}

func runLeader(ctx context.Context, deps cmddeps.Deps, result election.Result, link *devConsole, cfg *projectconfig.Config, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	file, err := dotenv.Load(cfg.Dir)
	if err != nil {
		return err
	}
	reportDotfile(stdout, cfg.Dir, file.Values, dotfileWatchedAdvice)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start dev server: %w", err)
	}

	addr := listener.Addr().String()
	devServerAddr := "http://" + addr

	run := invocation{name: "dev", local: link == nil}

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
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)
	defer httpSrv.Close()

	background, stopBackground := context.WithCancel(ctx)

	detecting := make(chan struct{})
	go func() {
		defer close(detecting)
		srv.RunDetector(background, func(err error) {
			fmt.Fprintln(stderr, "upload detection:", err)
		})
	}()
	defer func() {
		stopBackground()
		<-detecting
	}()

	if err := result.Claim(addr); err != nil {
		return err
	}
	defer func() { _ = result.Release() }()

	resolved, err := resolveOnce(ctx, srv, cfg, projectCfg.EnvVars, run, stdout, stderr)
	if err != nil {
		return err
	}
	watching, err := startWatching(background, srv, cfg, projectCfg.EnvVars, run, stdout, stderr)
	if err != nil {
		return fmt.Errorf("watch discovery paths: %w", err)
	}
	defer func() {
		stopBackground()
		<-watching.Done()
	}()
	srv.PushEnv(resolved)

	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), resolved)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	child, err := spawnAppChild(ctx, appCmd, stdin, deps.StdinIsTerminal(stdin))
	if err != nil {
		return err
	}
	return appExitError(ctx, child.wait())
}

func resolveOnce(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, projectEnv map[string]string, run invocation, stdout, stderr io.Writer) (map[string]string, error) {
	file, err := dotenv.Load(cfg.Dir)
	if err != nil {
		return nil, err
	}
	reportUnreadableLines(stdout, file.Unreadable)
	srv.UseValues(storeValues(projectEnv, file.Values), envwire.Scope(cfg, false, ""))
	return discoverAndSync(ctx, srv, cfg, file.Values, run, stdout, stderr)
}

func discoverAndSync(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, dotfile map[string]string, run invocation, stdout, stderr io.Writer) (map[string]string, error) {
	if err := srv.Discover(ctx, cfg, stdout, stderr); err != nil {
		return nil, refusedSync(srv, err, dotfile, run)
	}

	if err := srv.CheckEnv(ctx); err != nil {
		return nil, devRefusal(err, dotfileKeySet(dotfile), run)
	}

	appFolder := appbuilder.AppFolder(cfg.Apps)
	if err := checkStatableBinding(cfg.Apps, appFolder, filepath.Base(cfg.Path), srv.ScopedFolders()); err != nil {
		return nil, err
	}

	clientKeys, err := srv.ClientKeys()
	if err != nil {
		return nil, err
	}
	if err := generateClientAccessors(cfg, clientKeys); err != nil {
		return nil, err
	}

	syncResult := <-srv.Sync()
	if syncResult.Err != nil {
		if refusal := localResourceRefusal(syncResult.Err, dotfileKeySet(dotfile), run); refusal != nil {
			return nil, refusal
		}
		return nil, fmt.Errorf("sync failed: %w", syncResult.Err)
	}

	reportLiveValues(stdout, syncResult.LiveKeys, run)
	return resolvedEnv(syncResult.Account.EnvVars, syncResult.LiveValues, dotfile, syncResult.Resources, syncResult.DevServerAddress, appFolder), nil
}

func refusedSync(srv *devserver.Server, err error, dotfile map[string]string, run invocation) error {
	select {
	case result := <-srv.Sync():
		if refusal := localResourceRefusal(result.Err, dotfileKeySet(dotfile), run); refusal != nil {
			return refusal
		}
	default:
	}
	return err
}

func reportLiveValues(stdout io.Writer, liveKeys []string, run invocation) {
	if len(liveKeys) == 0 {
		return
	}
	keys := slices.Clone(liveKeys)
	slices.Sort(keys)
	if run.local {
		fmt.Fprintf(stdout, "resolved %s from %s, like every other value. Deployed, a rotated value is picked up within a bounded window.\n", strings.Join(keys, ", "), dotenv.FileName)
		return
	}
	fmt.Fprintf(stdout, "resolved %s once, at startup. Deployed, a rotated value is picked up within a bounded window; here, restart `%s` to pick one up.\n", strings.Join(keys, ", "), run.command())
}

func watchAndReResolve(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, projectEnv map[string]string, run invocation, stdout, stderr io.Writer) (*watcher.Watcher, error) {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	dirs, err := discovery.Dirs(roots)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	set := watcher.Set{Dirs: dirs, Files: []string{filepath.Join(cfg.Dir, dotenv.FileName)}}

	return watcher.Start(ctx, watcher.Config{Set: set, Debounce: watchDebounce, OnChange: func() {
		srv.ResetManifest()
		resolved, err := resolveOnce(ctx, srv, cfg, projectEnv, run, stdout, stderr)
		if err != nil {
			if ctx.Err() == nil {
				fmt.Fprintln(stderr, "re-resolve failed:", err)
			}
			return
		}
		srv.PushEnv(resolved)
	}, OnError: func(err error) {
		fmt.Fprintln(stderr, "watch error:", err)
	}})
}

func runFollower(ctx context.Context, deps cmddeps.Deps, leaderAddr string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	stream, err := subscribeEnv(ctx, leaderAddr)
	if err != nil {
		return fmt.Errorf("connect to leader: %w", err)
	}
	defer stream.close()

	first, err := stream.next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("connect to leader: stream closed before first env push")
		}
		return fmt.Errorf("connect to leader: %w", err)
	}

	child, err := startFollowerChild(ctx, deps, appArgs, first, stdin, stdout, stderr)
	if err != nil {
		return err
	}

	updates := make(chan map[string]string)
	streamDone := make(chan struct{}, 1)
	go func() {
		for {
			env, err := stream.next()
			if err != nil {
				streamDone <- struct{}{}
				return
			}
			select {
			case updates <- env:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case err := <-child.err:
			return appExitError(ctx, err)
		case env := <-updates:
			child.stop()
			child, err = startFollowerChild(ctx, deps, appArgs, env, stdin, stdout, stderr)
			if err != nil {
				return err
			}
		case <-streamDone:
			child.stop()
			if ctx.Err() != nil {
				return &exitsig.ExitError{Code: exitsig.InterruptCode}
			}
			fmt.Fprintln(stderr, "Leader disconnected. Restart `ocel dev` in the leader's terminal, then re-run this command.")
			return &exitsig.ExitError{Code: 1}
		}
	}
}

func startFollowerChild(ctx context.Context, deps cmddeps.Deps, appArgs []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (*appChild, error) {
	appCmd := exec.CommandContext(ctx, appArgs[0], appArgs[1:]...)
	appCmd.Env = applyEnv(os.Environ(), env)
	appCmd.Stdin = stdin
	appCmd.Stdout = stdout
	appCmd.Stderr = stderr
	return spawnAppChild(ctx, appCmd, stdin, deps.StdinIsTerminal(stdin))
}

func appExitError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &exitsig.ExitError{Code: exitsig.InterruptCode}
	}
	return waitExitError(err)
}

func waitExitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &exitsig.ExitError{Code: appExitCode(exitErr)}
	}
	return err
}

func mergeEnv(base []string, projectEnv, liveValues, dotfile map[string]string, resources []resolve.Resource, runtimeAddress, appFolder string) []string {
	return applyEnv(base, resolvedEnv(projectEnv, liveValues, dotfile, resources, runtimeAddress, appFolder))
}

func resolvedEnv(projectEnv, liveValues, dotfile map[string]string, resources []resolve.Resource, runtimeAddress, appFolder string) map[string]string {
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
	if runtimeAddress != "" {
		merged[constants.RuntimeAddressEnvName] = runtimeAddress
	}
	merged[constants.AppFolderEnvName] = appFolder
	merged[constants.AppURLEnvName] = localURL(merged[portEnv])
	merged[providerkit.ClientURLEnvName] = merged[constants.AppURLEnvName]
	return merged
}

func localURL(port string) string {
	return "http://localhost:" + cmp.Or(port, os.Getenv(portEnv), defaultDevPort)
}

const portEnv = "PORT"

const defaultDevPort = "3000"

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
