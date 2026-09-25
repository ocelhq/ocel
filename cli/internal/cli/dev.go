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
	"github.com/ocelhq/ocel/cli/internal/devlock"
	"github.com/ocelhq/ocel/cli/internal/devserver"
	"github.com/ocelhq/ocel/cli/internal/devstack"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/election"
	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/envwire"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/watcher"
	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

var (
	watchDebounce = 300 * time.Millisecond
	startWatching = watchAndReResolve
)

var devReset bool

var devCmd = &cobra.Command{
	Use:   "dev -- <command> [args...]",
	Short: "Run your project in development mode",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		ctx, stop := installDevInterruptHandler(cmd.Context(), cmd.ErrOrStderr())
		defer stop()

		return runDev(ctx, newDeps(), devReset, cwd, args, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	devCmd.Flags().BoolVar(&devReset, "reset", false, "Wipe the data this project's dev resources have kept, then start from empty ones")
}

func runDev(ctx context.Context, deps cmddeps.Deps, reset bool, cwd string, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	// TODO: unlike build/deploy, this never calls runtrace.Start, so discovery
	// below produces no spans or logs and nothing else says so.
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
			if reset {
				return errors.New("`ocel dev` is already running for this project and owns its dev resources: stop it, then run `ocel dev --reset`")
			}
			return runFollower(ctx, deps, role.Leader, appArgs, stdout, stderr, stdin)
		}

		if err := runLeader(ctx, deps, role, reset, cfg, appArgs, stdout, stderr, stdin); !errors.Is(err, election.ErrLost) {
			return err
		}
	}
	return errors.New("determine leader/follower role: repeatedly lost the leader election; try again")
}

func runLeader(ctx context.Context, deps cmddeps.Deps, result election.Result, reset bool, cfg *projectconfig.Config, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	source, err := readDevSource(ctx, cfg)
	if err != nil {
		return err
	}
	values, err := source.read(cfg.Dir)
	if err != nil {
		return err
	}
	reportDevValues(stdout, cfg.Dir, values, true)

	if reset {
		if err := devstack.Reset(ctx, deps.OpenDocker, devStateDir(cfg), devstack.ProjectName(cfg.Dir)); err != nil {
			return err
		}
	}

	host, err := startDevHost(ctx, deps, cfg, source, stdout, stderr)
	if err != nil {
		return err
	}
	claimed := false
	defer func() {
		host.close()
		if claimed {
			_ = result.Release()
		}
	}()
	srv := host.srv
	run := invocation{name: "dev", source: source}

	background, stopBackground := context.WithCancel(ctx)
	defer stopBackground()

	if err := result.Claim(devlock.Lease{Addr: host.addr, Token: srv.AppToken()}); err != nil {
		return err
	}
	claimed = true

	resolved, err := resolveOnce(ctx, srv, cfg, run, stdout, stderr)
	if err != nil {
		return err
	}
	watching, err := startWatching(background, srv, cfg, run, stdout, stderr)
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

func resolveOnce(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, run invocation, stdout, stderr io.Writer) (map[string]string, error) {
	values, err := run.source.read(cfg.Dir)
	if err != nil {
		return nil, err
	}
	reportUnreadableLines(stdout, values)
	srv.UseValues(values.merged(), envwire.Scope(cfg, false, ""))
	return discoverAndSync(ctx, srv, cfg, values, envwire.DevScope(cfg), run, stdout, stderr)
}

func targetScope(cfg *projectconfig.Config, cwd string) envgate.Scope {
	scope := envwire.DevScope(cfg)
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
		scope.Apps = []envgate.App{scope.Apps[target]}
	}
	return scope
}

func discoverAndSync(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, values devValues, scope envgate.Scope, run invocation, stdout, stderr io.Writer) (map[string]string, error) {
	if err := srv.Discover(ctx, cfg, stdout, stderr); err != nil {
		return nil, refusedSync(srv, err)
	}

	if err := srv.CheckEnv(ctx); err != nil {
		return nil, devRefusal(err, values.keys(), run)
	}

	appFolder := appbuilder.AppFolder(cfg.Apps)
	if err := checkStatableBinding(cfg.Apps, appFolder, filepath.Base(cfg.Path), srv.ScopedFolders()); err != nil {
		return nil, err
	}

	clientKeys, err := srv.ClientKeys()
	if err != nil {
		return nil, err
	}
	if _, err := generateClientAccessors(cfg, clientKeys); err != nil {
		return nil, err
	}

	syncResult := <-srv.Sync()
	if syncResult.Err != nil {
		return nil, fmt.Errorf("sync failed: %w", syncResult.Err)
	}

	reportLiveValues(stdout, syncResult.LiveKeys)
	return resolvedEnv(syncResult.LiveValues, values.merged(), syncResult.Resources, runtimeAccess{address: syncResult.DevServerAddress, token: syncResult.AppToken}, appFolder, scope), nil
}

func refusedSync(srv *devserver.Server, err error) error {
	select {
	case result := <-srv.Sync():
		if result.Err != nil {
			return result.Err
		}
	default:
	}
	return err
}

type devHost struct {
	srv   *devserver.Server
	addr  string
	close func()
}

func startDevHost(ctx context.Context, deps cmddeps.Deps, cfg *projectconfig.Config, source devSource, stdout, stderr io.Writer) (*devHost, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start dev server: %w", err)
	}
	addr := listener.Addr().String()

	stack := devstack.New(devstack.ProjectName(cfg.Dir), devstack.Env{
		Open:       deps.OpenDocker,
		StateDir:   devStateDir(cfg),
		AppOrigins: devAppOrigins(cfg.Dir, source),
		Stdout:     stdout,
		Report:     func(err error) { fmt.Fprintln(stderr, "dev resources:", err) },
	})

	srv := devserver.New("http://"+addr, stack)
	httpSrv := &http.Server{Handler: srv.Mux()}
	go httpSrv.Serve(listener)

	return &devHost{srv: srv, addr: addr, close: func() {
		_ = httpSrv.Close()
		stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), devStackStopsWithin)
		defer cancel()
		if err := stack.Close(stopping); err != nil {
			fmt.Fprintln(stderr, "stop dev resources:", err)
		}
	}}, nil
}

func devStateDir(cfg *projectconfig.Config) string {
	return filepath.Join(cfg.Dir, constants.ProjectStateDirName, "devstack")
}

func devAppOrigins(dir string, source devSource) func() []string {
	return func() []string {
		var held string
		if values, err := source.read(dir); err == nil {
			held = values.merged()[portEnv]
		}
		port := cmp.Or(held, os.Getenv(portEnv), defaultDevPort)
		return []string{"http://localhost:" + port, "http://127.0.0.1:" + port}
	}
}

func reportLiveValues(stdout io.Writer, liveKeys []string) {
	if len(liveKeys) == 0 {
		return
	}
	keys := slices.Clone(liveKeys)
	slices.Sort(keys)
	fmt.Fprintf(stdout, "resolved %s the way dev resolves every other value. Deployed, a rotated value is picked up within a bounded window.\n", strings.Join(keys, ", "))
}

func watchAndReResolve(ctx context.Context, srv *devserver.Server, cfg *projectconfig.Config, run invocation, stdout, stderr io.Writer) (*watcher.Watcher, error) {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	dirs, err := discovery.Dirs(roots)
	if err != nil {
		return nil, fmt.Errorf("resolve watch directories: %w", err)
	}

	set := watcher.Set{Dirs: dirs}
	for _, name := range run.source.files() {
		set.Files = append(set.Files, filepath.Join(cfg.Dir, name))
	}

	return watcher.Start(ctx, watcher.Config{Set: set, Debounce: watchDebounce, OnChange: func() {
		srv.ResetManifest()
		resolved, err := resolveOnce(ctx, srv, cfg, run, stdout, stderr)
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

func runFollower(ctx context.Context, deps cmddeps.Deps, leader devlock.Lease, appArgs []string, stdout, stderr io.Writer, stdin io.Reader) error {
	stream, err := subscribeEnv(ctx, leader)
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

type runtimeAccess struct {
	address string
	token   string
}

func mergeEnv(base []string, liveValues, values map[string]string, resources []resolve.Resource, runtime runtimeAccess, appFolder string, scope envgate.Scope) []string {
	return applyEnv(base, resolvedEnv(liveValues, values, resources, runtime, appFolder, scope))
}

func resolvedEnv(liveValues, values map[string]string, resources []resolve.Resource, runtime runtimeAccess, appFolder string, scope envgate.Scope) map[string]string {
	merged := make(map[string]string, len(liveValues)+len(values)+1)
	for k, v := range liveValues {
		merged[k] = v
	}
	for k, v := range values {
		merged[k] = v
	}
	for _, r := range resources {
		for k, v := range r.Env {
			merged[k] = v
		}
	}
	if runtime.address != "" {
		merged[constants.RuntimeAddressEnvName] = runtime.address
		merged[channel.SessionTokenEnvVar] = runtime.token
	}
	merged[constants.AppFolderEnvName] = appFolder
	merged[constants.AppURLEnvName] = localURL(merged[portEnv])
	if scope.OcelWrites(providerkit.ClientURLEnvName, nil) {
		merged[providerkit.ClientURLEnvName] = merged[constants.AppURLEnvName]
	}
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
