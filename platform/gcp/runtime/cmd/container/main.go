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
	"syscall"
	"time"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/processenv"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/pkg/runtime/child"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	variables "github.com/ocelhq/ocel/platform/gcp/provider/live"
	"github.com/ocelhq/ocel/platform/gcp/runtime/bucket"
	source "github.com/ocelhq/ocel/platform/gcp/runtime/live"
	realtimeproxy "github.com/ocelhq/ocel/platform/realtime/proxy"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const (
	stopGrace       = 10 * time.Second
	drainGrace      = 5 * time.Second
	forwardedSignal = "ocel: forwarding %s to the app\n"
)

var forwarded = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Environ(), fileExists))
}

func run(ctx context.Context, command []string, environ []string, present func(path string) bool) int {
	if len(command) == 0 {
		return fatal("the image names no command for the runtime to run: it must set an ENTRYPOINT or CMD")
	}
	exposed := containerimage.PortText
	env := make([]string, 0, len(environ))
	manifest := ""
	healthPath := ""
	worker := ""
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case containerimage.PortEnvVar:
			exposed = value
			continue
		case variables.EnvVar:
			manifest = value
			continue
		case originguard.HealthPathVar:
			healthPath = value
		case processenv.WorkerEnvVar:
			worker = value
		}
		env = append(env, entry)
	}
	guard, env := originguard.GuardFromEnv(env)

	pinned, err := readManifest(manifest)
	if err != nil {
		return fatal(err.Error())
	}
	values, err := resolve(ctx, manifest)
	if err != nil {
		return fatal(err.Error())
	}
	if worker != "" {
		command = containerimage.WorkerCommand(present, command)
	}

	internal, err := child.FreePort()
	if err != nil {
		return fatal(fmt.Sprintf("find a loopback port for the app: %v", err))
	}
	fronting, err := serveProxy(ctx, values, pinned, "127.0.0.1:"+strconv.Itoa(internal))
	if err != nil {
		return fatal(err.Error())
	}
	defer fronting.Close()

	env = append(env, containerimage.PortEnvVar+"="+strconv.Itoa(internal))
	env = append(env, values.Env()...)
	env = append(env, fronting.Env...)
	if worker == "" {
		env = containerimage.AppendNextServerPreload(present, env)
	}

	proc, err := child.Start(child.Options{Command: command, Env: env, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		return fatal(err.Error())
	}

	listening := child.WatchListening("127.0.0.1:"+strconv.Itoa(internal), nil)
	upstream := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(internal)}
	front := originguard.Handler(originguard.Options{
		Upstream:   upstream,
		Guard:      guard,
		HealthPath: healthPath,
	})
	if worker != "" {
		if front, err = workerFront(pinned, worker, upstream.String()); err != nil {
			_ = proc.Stop(stopGrace)
			return fatal(err.Error())
		}
	}
	var server *http.Server
	served := make(chan error, 1)

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
		case err := <-listening:
			listening = nil
			if err != nil {
				continue
			}
			ln, err := net.Listen("tcp", ":"+exposed)
			if err != nil {
				_ = proc.Stop(stopGrace)
				return fatal(fmt.Sprintf("listen on port %s: %v", exposed, err))
			}
			server = &http.Server{Handler: front}
			go func() { served <- server.Serve(ln) }()
		case err := <-served:
			if !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(os.Stderr, "ocel: the front stopped serving: %v\n", err)
			}
			exit := proc.Stop(stopGrace)
			return exitCode(exit)
		case exit := <-proc.Exited():
			if exit.Err != nil {
				fmt.Fprintf(os.Stderr, "ocel: the app (%s) exited: %v\n", proc.Program(), exit.Err)
			}
			if server != nil {
				drain, cancel := context.WithTimeout(context.Background(), drainGrace)
				_ = server.Shutdown(drain)
				cancel()
			}
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

func readManifest(raw string) (variables.Manifest, error) {
	if raw == "" {
		return variables.Manifest{}, nil
	}
	manifest, err := variables.Parse([]byte(raw))
	if err != nil {
		return variables.Manifest{}, fmt.Errorf("read this deployment's manifest: %w", err)
	}
	return manifest, nil
}

func serveProxy(ctx context.Context, values *live.Values, manifest variables.Manifest, app string) (bindingproxy.Served, error) {
	var services bindingproxy.Services
	if values != nil && bindsBuckets(values) {
		store, err := bucket.Open(ctx, manifest.Endpoint)
		if err != nil {
			return bindingproxy.Served{}, err
		}
		if services.Buckets, err = bucket.NewDispatch(ctx, store, values, s3store.HTTPPoster{App: app}); err != nil {
			return bindingproxy.Served{}, err
		}
	}
	if manifest.Tasks != nil {
		deployment := deploymentOf(manifest)
		services.Tasks, services.Topics = deployment.Tasks(), deployment.Topics()
	}
	if values != nil && manifest.RealtimePublishURL != "" {
		services.Realtime = realtimeproxy.NewService(values, realtimeproxy.NewGatewayTransport(http.DefaultClient, manifest.RealtimePublishURL))
	}
	if services.Empty() {
		return bindingproxy.Served{}, nil
	}
	return bindingproxy.Serve(services)
}

func bindsBuckets(values *live.Values) bool {
	return slices.ContainsFunc(values.Bindings(), func(bound live.Binding) bool {
		return bound.Type == bindingsv1.BindingType_BINDING_TYPE_BUCKET
	})
}

func resolve(ctx context.Context, manifest string) (*live.Values, error) {
	if manifest == "" {
		return nil, nil
	}
	values, err := source.FromManifest([]byte(manifest))
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
	if err := os.MkdirAll(containerimage.LivePath, 0o700); err != nil {
		return nil, fmt.Errorf("make %s to hand the app its live variables through: %w", containerimage.LivePath, err)
	}
	if err := values.Project(containerimage.LivePath); err != nil {
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
