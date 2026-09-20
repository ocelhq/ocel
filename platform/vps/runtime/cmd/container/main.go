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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/runtimekit/child"
	"github.com/ocelhq/ocel/pkg/runtimekit/front"
	rt "github.com/ocelhq/ocel/pkg/runtimekit/live"
	"github.com/ocelhq/ocel/pkg/runtimekit/proxy"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/runtime/bucket"
	"github.com/ocelhq/ocel/platform/vps/runtime/live"
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
		return fatal("the image names no command for the runtime to run: it must carry an ENTRYPOINT or CMD")
	}
	exposed := providerkit.InjectedPortText
	env := make([]string, 0, len(environ))
	manifest := ""
	healthPath := ""
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case providerkit.InjectedPortName:
			exposed = value
			continue
		case vars.EnvVar:
			manifest = value
			continue
		case front.HealthPathVar:
			healthPath = value
		}
		env = append(env, entry)
	}
	guard, env := front.GuardFromEnv(env)

	values, err := resolve(ctx, manifest, vars.SocketPath, vars.ProjectionDir)
	if err != nil {
		return fatal(err.Error())
	}

	pinned, err := pinned(manifest)
	if err != nil {
		return fatal(err.Error())
	}
	fronting, err := proxying(pinned, values, vars.SocketPath)
	if err != nil {
		return fatal(err.Error())
	}
	defer fronting.Close()

	internal, err := child.FreePort()
	if err != nil {
		return fatal(fmt.Sprintf("find a loopback port for the app: %v", err))
	}
	env = append(env, providerkit.InjectedPortName+"="+strconv.Itoa(internal))
	env = append(env, values.Env()...)
	env = append(env, fronting.Env...)

	proc, err := child.Start(child.Options{Command: command, Env: env, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		return fatal(err.Error())
	}

	var ready atomic.Bool
	listening := child.WatchListening("127.0.0.1:"+strconv.Itoa(internal), nil)
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
	upstream := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(internal)}
	server := &http.Server{Handler: front.Handler(front.Options{
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

func proxying(manifest vars.Manifest, values *rt.Values, socket string) (proxy.Served, error) {
	if manifest.Store == nil || !slices.ContainsFunc(manifest.Bindings, func(l rt.Binding) bool { return naming.Proxied(l.Type) }) {
		return proxy.Served{}, nil
	}
	secret := values.Value(vars.StoreSecretKey)
	if secret == "" {
		return proxy.Served{}, fmt.Errorf(
			"this deployment binds a bucket and the box handed its runtime no credential for the store at %s, so nothing it wrote could be signed",
			manifest.Store.Endpoint)
	}
	internal := bucket.Store{
		Endpoint:        manifest.Store.Endpoint,
		Region:          manifest.Store.Region,
		AccessKeyID:     manifest.Store.AccessKeyID,
		SecretAccessKey: secret,
		PathStyle:       manifest.Store.PathStyle,
	}
	cfg := bucket.Config{
		Objects:      internal.Client(),
		Internal:     internal.Presigner(),
		External:     publishing(internal, manifest.Store.PublicBaseURL, values),
		Callbacks:    bucket.HTTPPoster{},
		PostPolicies: true,
		Sessions:     manifest.Store.Sessions,
	}
	if manifest.Store.Volume != "" {
		cfg.Volume = live.FreeSpace(socket)
	}
	return proxy.Serve(bucket.New(cfg))
}

func publishing(store bucket.Store, configured string, values *rt.Values) func() (bucket.PresignAPI, string) {
	var mu sync.Mutex
	var base string
	var signer bucket.PresignAPI
	return func() (bucket.PresignAPI, string) {
		now := configured
		if claimed := values.Value(vars.StorePublicKey); claimed != "" {
			now = claimed
		}
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

func resolve(ctx context.Context, manifest, socket, dir string) (*rt.Values, error) {
	if manifest == "" {
		return nil, nil
	}
	values, err := live.FromManifest([]byte(manifest), socket)
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
		return nil, fmt.Errorf("nothing is stored for %s, which this deployment declares as %s: set it with `ocel env set` and deploy again",
			strings.Join(missing, ", "), plural(len(missing), "a secret", "secrets"))
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
