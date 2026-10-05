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

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/realtime/v1/realtimev1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/pkg/runtime/child"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	realtimeproxy "github.com/ocelhq/ocel/platform/realtime/proxy"
	s3store "github.com/ocelhq/ocel/platform/s3"
	boxlive "github.com/ocelhq/ocel/platform/vps/provider/live"
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
	command = chooseCommand(environ, command, isFile)
	read, env := readFront(environ)
	exposed, manifest, healthPath, guard := read.exposed, read.manifest, read.healthPath, read.guard

	values, err := resolve(ctx, manifest, boxlive.SocketPath, containerimage.LivePath)
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
	var ready atomic.Bool
	front, err := newFront(pinned, originguard.Options{
		Upstream:   &url.URL{Scheme: "http", Host: app},
		Guard:      guard,
		HealthPath: healthPath,
		Ready:      ready.Load,
	})
	if err != nil {
		return fatal(err.Error())
	}

	fronting, err := proxying(pinned, values, boxlive.SocketPath, app)
	if err != nil {
		return fatal(err.Error())
	}
	defer fronting.Close()

	env = append(env, containerimage.PortEnvVar+"="+strconv.Itoa(internal))
	env = append(env, values.Env()...)
	env = append(env, fronting.Env...)

	proc, err := child.Start(child.Options{Command: command, Env: env, Stdout: os.Stdout, Stderr: os.Stderr})
	if err != nil {
		return fatal(err.Error())
	}

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
	server := &http.Server{Handler: front}
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
				fmt.Fprintf(os.Stderr, "ocel: the app (%s) exited: %v\n", proc.Command(), exit.Err)
			}
			drain, cancel := context.WithTimeout(context.Background(), drainGrace)
			_ = server.Shutdown(drain)
			cancel()
			return exitCode(exit)
		}
	}
}

type front struct {
	exposed    string
	manifest   string
	healthPath string
	guard      *originguard.Guard
}

func readFront(environ []string) (front, []string) {
	read := front{exposed: containerimage.PortText}
	env := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case containerimage.PortEnvVar:
			read.exposed = value
			continue
		case boxlive.EnvVar:
			read.manifest = value
			continue
		case originguard.HealthPathVar:
			read.healthPath = value
		}
		env = append(env, entry)
	}
	read.guard, env = originguard.GuardFromEnv(env)
	return read, env
}

func chooseCommand(environ, command []string, present func(path string) bool) []string {
	if !slices.ContainsFunc(environ, func(entry string) bool { return strings.HasPrefix(entry, processenv.WorkerEnvVar+"=") }) {
		return command
	}
	return containerimage.WorkerCommand(present, command)
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func exitCode(exit child.Exit) int {
	if exit.Code == 0 && exit.Err != nil {
		return 1
	}
	return exit.Code
}

func pinned(manifest string) (boxlive.Manifest, error) {
	if manifest == "" {
		return boxlive.Manifest{}, nil
	}
	return boxlive.Parse([]byte(manifest))
}

func proxying(manifest boxlive.Manifest, values *live.Values, socket, app string) (bindingproxy.Served, error) {
	if values == nil {
		return bindingproxy.Served{}, nil
	}
	buckets, err := newBuckets(manifest, values, socket, app)
	if err != nil {
		return bindingproxy.Served{}, err
	}
	services := bindingproxy.Services{Buckets: buckets, Realtime: newRealtime(manifest, values)}
	if manifest.Queue != "" {
		agent := source.Client(socket)
		services.Tasks = taskv1connect.NewTaskServiceClient(agent, source.AgentURL)
		services.Topics = topicv1connect.NewTopicServiceClient(agent, source.AgentURL)
	}
	if services.Empty() {
		return bindingproxy.Served{}, nil
	}
	return bindingproxy.Serve(services)
}

func newRealtime(manifest boxlive.Manifest, values *live.Values) realtimev1connect.RealtimeServiceHandler {
	if manifest.RealtimePublishURL == "" {
		return nil
	}
	return realtimeproxy.NewService(values, realtimeproxy.NewGatewayTransport(http.DefaultClient, manifest.RealtimePublishURL))
}

func newBuckets(manifest boxlive.Manifest, values *live.Values, socket, app string) (bucketv1connect.BucketServiceHandler, error) {
	if manifest.Store == nil {
		return s3store.NewBoundDispatch(values, app), nil
	}
	secret := values.Value(boxlive.StoreSecretKey)
	if secret == "" {
		return nil, fmt.Errorf("this deployment binds a bucket but has no credential for the store at %s", manifest.Store.Endpoint)
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
	return s3store.NewDispatch(s3store.New(cfg), values, cfg.Callbacks), nil
}

const unclaimedWindow = 10 * time.Second

func publishing(store s3store.Store, values *live.Values) func(context.Context) (s3store.PresignAPI, string) {
	var mu sync.Mutex
	var base string
	var signer s3store.PresignAPI
	var looked time.Time
	return func(ctx context.Context) (s3store.PresignAPI, string) {
		claimed := values.Value(boxlive.StorePublicKey)
		if claimed == "" {
			mu.Lock()
			stale := time.Since(looked) >= unclaimedWindow
			if stale {
				looked = time.Now()
			}
			mu.Unlock()
			if stale {
				values.Reread(ctx)
				claimed = values.Value(boxlive.StorePublicKey)
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
