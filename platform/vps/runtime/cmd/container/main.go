package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/runtimekit/bindingproxy"
	"github.com/ocelhq/ocel/pkg/runtimekit/child"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
	s3store "github.com/ocelhq/ocel/platform/s3"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
	source "github.com/ocelhq/ocel/platform/vps/runtime/live"
)

const (
	stopGrace       = 10 * time.Second
	drainGrace      = 5 * time.Second
	forwardedSignal = "ocel: forwarding %s to the app\n"
)

var forwarded = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Environ()))
}

func run(ctx context.Context, command []string, environ []string) int {
	if len(command) == 0 {
		return fatal("the image has no ENTRYPOINT or CMD")
	}
	exposed := appbuild.InjectedPortText
	env := make([]string, 0, len(environ))
	manifest := ""
	healthPath := ""
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case appbuild.InjectedPortName:
			exposed = value
			continue
		case vars.EnvVar:
			manifest = value
			continue
		case originguard.HealthPathVar:
			healthPath = value
		}
		env = append(env, entry)
	}
	guard, env := originguard.GuardFromEnv(env)

	values, err := resolve(ctx, manifest, vars.SocketPath, appbuild.ContainerLivePath)
	if err != nil {
		return fatal(err.Error())
	}

	pinned, err := pinned(manifest)
	if err != nil {
		return fatal(err.Error())
	}
	internal, err := child.FreePort()
	if err != nil {
		return fatal(fmt.Sprintf("find a loopback port for the app: %v", err))
	}
	app := "127.0.0.1:" + strconv.Itoa(internal)

	fronting, err := proxying(pinned, values, vars.SocketPath, app)
	if err != nil {
		return fatal(err.Error())
	}
	defer fronting.Close()

	env = append(env, appbuild.InjectedPortName+"="+strconv.Itoa(internal))
	env = append(env, values.Env()...)
	env = append(env, fronting.Env...)

	proc, err := child.Start(child.Options{Command: command, Env: env, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		return fatal(err.Error())
	}

	var ready atomic.Bool
	listening := child.WatchListening(app, nil)
	go func() {
		if err := <-listening; err == nil {
			ready.Store(true)
		}
	}()

	ln, err := net.Listen("tcp", ":"+exposed)
	if err != nil {
		_ = proc.Stop(stopGrace)
		return fatal(fmt.Sprintf("listen on port %s: %v", exposed, err))
	}
	upstream := &url.URL{Scheme: "http", Host: app}
	server := &http.Server{Handler: originguard.Handler(originguard.Options{
		Upstream:   upstream,
		Guard:      guard,
		HealthPath: healthPath,
		Ready:      ready.Load,
	})}
	served := make(chan error, 1)
	go func() { served <- server.Serve(ln) }()

	keep, stopKeeping := context.WithCancel(ctx)
	defer stopKeeping()
	go values.Keep(keep)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, forwarded...)
	defer signal.Stop(signals)

	for {
		select {
		case sig := <-signals:
			fmt.Fprintf(os.Stderr, forwardedSignal, sig)
			if err := proc.Signal(sig); err != nil {
				fmt.Fprintf(os.Stderr, "ocel: could not forward %s: %v\n", sig, err)
			}
		case err := <-served:
			if !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "ocel: the front stopped serving: %v\n", err)
			}
			exit := proc.Stop(stopGrace)
			return exitCode(exit)
		case exit := <-proc.Exited():
			if exit.Err != nil {
				fmt.Fprintf(os.Stderr, "ocel: the app exited: %v\n", exit.Err)
			}
			drain, cancel := context.WithTimeout(context.Background(), drainGrace)
			_ = server.Shutdown(drain)
			cancel()
			return exitCode(exit)
		}
	}
}

func exitCode(exit child.Exit) int {
	if exit.Code == 0 && exit.Err != nil {
		return 1
	}
	return exit.Code
}

func pinned(manifest string) (vars.Manifest, error) {
	if manifest == "" {
		return vars.Manifest{}, nil
	}
	return vars.Parse([]byte(manifest))
}

func proxying(manifest vars.Manifest, values *live.Values, socket, app string) (bindingproxy.Served, error) {
	if values == nil {
		return bindingproxy.Served{}, nil
	}
	if manifest.Store == nil {
		return s3store.ServeBound(values, app)
	}
	secret := values.Value(vars.StoreSecretKey)
	if secret == "" {
		return bindingproxy.Served{}, fmt.Errorf("this deployment binds a bucket but has no credential for the store at %s", manifest.Store.Endpoint)
	}
	internal := s3store.Store{
		Endpoint:        manifest.Store.Endpoint,
		Region:          manifest.Store.Region,
		AccessKeyID:     manifest.Store.AccessKeyID,
		SecretAccessKey: secret,
		PathStyle:       manifest.Store.PathStyle,
	}
	cfg := s3store.Config{
		Objects:      internal.Client(),
		Internal:     internal.Presigner(),
		External:     publishing(internal, values),
		Callbacks:    s3store.HTTPPoster{App: app},
		PostPolicies: true,
		SweepUploads: manifest.Store.SweepUploads,
		Sessions:     manifest.Store.Sessions,
		Granted:      manifest.Store.Granted,
	}
	if manifest.Store.Volume != "" {
		cfg.Volume = source.FreeSpace(socket)
	}
	return bindingproxy.Serve(s3store.RouteRecords(s3store.New(cfg), values, cfg.Callbacks))
}

const unclaimedWindow = 10 * time.Second

func publishing(store s3store.Store, values *live.Values) func(context.Context) (s3store.PresignAPI, string) {
	var mu sync.Mutex
	var base string
	var signer s3store.PresignAPI
	var looked time.Time
	return func(ctx context.Context) (s3store.PresignAPI, string) {
		claimed := values.Value(vars.StorePublicKey)
		if claimed == "" {
			mu.Lock()
			stale := time.Since(looked) >= unclaimedWindow
			if stale {
				looked = time.Now()
			}
			mu.Unlock()
			if stale {
				values.Reread(ctx)
				claimed = values.Value(vars.StorePublicKey)
			}
		}
		now := claimed
		if now == "" {
			return nil, ""
		}
		mu.Lock()
		defer mu.Unlock()
		if now != base {
			published := store
			published.Endpoint = now
			base, signer = now, published.Presigner()
		}
		return signer, base
	}
}

func resolve(ctx context.Context, manifest, socket, dir string) (*live.Values, error) {
	if manifest == "" {
		return nil, nil
	}
	values, err := source.FromManifest([]byte(manifest), socket)
	if err != nil {
		return nil, fmt.Errorf("read this deployment's live variables: %w", err)
	}
	if values == nil {
		return nil, nil
	}
	if err := values.Join(values.Prefetch(ctx)); err != nil {
		return nil, fmt.Errorf("resolve this deployment's live variables: %w", err)
	}
	if missing := values.Missing(); len(missing) > 0 {
		return nil, fmt.Errorf("no value is set for %s %s\nSet it with `ocel env set` and deploy again",
			plural(len(missing), "secret", "secrets"), strings.Join(missing, ", "))
	}
	if err := values.Project(dir); err != nil {
		return nil, err
	}
	return values, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func fatal(msg string) int {
	fmt.Fprintln(os.Stderr, "ocel: "+msg)
	return 1
}
