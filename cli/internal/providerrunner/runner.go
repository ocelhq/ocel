package providerrunner

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/procgroup"
	"github.com/ocelhq/ocel/pkg/channel"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
	"github.com/ocelhq/ocel/pkg/proto/env/v1/envv1connect"
)

const DefaultReadyTimeout = 10 * time.Second

const ReadyTimeoutEnvVar = "OCEL_READY_TIMEOUT"

const DefaultGracePeriod = 2 * time.Second

const DefaultReapTimeout = 2 * time.Second

type Config struct {
	BinaryPath   string
	Args         []string
	Env          []string
	Provider     *deploymentsv1.ProviderConfig
	Stdout       io.Writer
	Stderr       io.Writer
	ReadyTimeout time.Duration
	GracePeriod  time.Duration
	ReapTimeout  time.Duration
}

type EarlyExitError struct {
	Err    error
	Stderr string
}

func (e *EarlyExitError) Error() string {
	msg := "provider exited before signaling readiness"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if stderr := strings.TrimSpace(e.Stderr); stderr != "" {
		msg += "\n" + stderr
	}
	return msg
}

func (e *EarlyExitError) Unwrap() error { return e.Err }

type ReadyTimeoutError struct {
	Timeout time.Duration
}

func (e *ReadyTimeoutError) Error() string {
	return fmt.Sprintf("provider did not signal readiness within %s", e.Timeout)
}

type DeployFailedError struct {
	Message string
}

func (e *DeployFailedError) Error() string {
	if strings.TrimSpace(e.Message) == "" {
		return "the provider reported a failure without a reason"
	}
	return e.Message
}

type Runner struct {
	cmd          *exec.Cmd
	token        string
	provider     *deploymentsv1.ProviderConfig
	stdout       io.Writer
	stderr       io.Writer
	readyTimeout time.Duration
	gracePeriod  time.Duration
	reapTimeout  time.Duration

	readyCh chan string
	scanErr chan error
	done    chan struct{}
	waitErr error

	stderrMu  sync.Mutex
	stderrBuf bytes.Buffer

	outMu sync.Mutex

	mu               sync.Mutex
	network, address string
	client           deploymentsv1connect.DeploymentServiceClient
	vars             envv1connect.EnvVarsServiceClient

	closeOnce sync.Once
}

func Spawn(ctx context.Context, cfg Config) (*Runner, error) {
	if cfg.BinaryPath == "" {
		return nil, errors.New("providerrunner: BinaryPath is required")
	}

	token, err := newSessionToken()
	if err != nil {
		return nil, err
	}

	base := cfg.Env
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+1)
	env = append(env, base...)
	env = append(env, channel.SessionTokenEnvVar+"="+token)

	cmd := exec.Command(cfg.BinaryPath, cfg.Args...)
	cmd.Env = env
	procgroup.New(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("providerrunner: attach stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("providerrunner: attach stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("providerrunner: spawn provider %q: %w", cfg.BinaryPath, err)
	}

	r := &Runner{
		cmd:          cmd,
		token:        token,
		provider:     cfg.Provider,
		stdout:       cfg.Stdout,
		stderr:       cfg.Stderr,
		readyTimeout: resolveReadyTimeout(cfg.ReadyTimeout),
		gracePeriod:  resolveDuration(cfg.GracePeriod, DefaultGracePeriod),
		reapTimeout:  resolveDuration(cfg.ReapTimeout, DefaultReapTimeout),
		readyCh:      make(chan string, 1),
		scanErr:      make(chan error, 1),
		done:         make(chan struct{}),
	}

	registerLive(r)

	var drainWG sync.WaitGroup
	drainWG.Add(2)
	go func() { defer drainWG.Done(); r.drainStdout(stdoutPipe) }()
	go func() { defer drainWG.Done(); r.drainStderr(stderrPipe) }()
	go func() {
		drainWG.Wait()
		r.waitErr = cmd.Wait()
		_ = procgroup.Kill(cmd)
		deregisterLive(r)
		close(r.done)
	}()

	go func() {
		select {
		case <-ctx.Done():
			r.Close()
		case <-r.done:
		}
	}()

	return r, nil
}

func resolveReadyTimeout(override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	if v := os.Getenv(ReadyTimeoutEnvVar); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return DefaultReadyTimeout
}

func resolveDuration(override, def time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return def
}

func (r *Runner) Ready(ctx context.Context) error {
	timer := time.NewTimer(r.readyTimeout)
	defer timer.Stop()

	select {
	case addr := <-r.readyCh:
		return r.open(ctx, addr)
	case err := <-r.scanErr:
		return err
	case <-r.done:
		select {
		case addr := <-r.readyCh:
			return r.open(ctx, addr)
		default:
		}
		r.stderrMu.Lock()
		stderr := r.stderrBuf.String()
		r.stderrMu.Unlock()
		return &EarlyExitError{Err: r.waitErr, Stderr: stderr}
	case <-timer.C:
		return &ReadyTimeoutError{Timeout: r.readyTimeout}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) open(ctx context.Context, addr string) error {
	if err := r.dial(addr); err != nil {
		return err
	}
	return r.configure(ctx)
}

func (r *Runner) configure(ctx context.Context) error {
	if r.provider == nil {
		return nil
	}
	client, err := r.Deployments()
	if err != nil {
		return err
	}
	if _, err := client.Configure(ctx, &deploymentsv1.ConfigureRequest{Config: r.provider}); err != nil {
		return fmt.Errorf("providerrunner: configure the provider session: %w", err)
	}
	return nil
}

func (r *Runner) dial(addr string) error {
	network, address, err := channel.ParseAddr(addr)
	if err != nil {
		return fmt.Errorf("providerrunner: parse readiness address: %w", err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, address)
			},
		},
	}

	auth := connect.WithInterceptors(authInterceptor{token: r.token}, traceParentInterceptor{})

	r.mu.Lock()
	defer r.mu.Unlock()
	r.network, r.address = network, address
	r.client = deploymentsv1connect.NewDeploymentServiceClient(httpClient, "http://provider", auth)
	r.vars = envv1connect.NewEnvVarsServiceClient(httpClient, "http://provider", auth)
	return nil
}

func (r *Runner) Vars() (envv1connect.EnvVarsServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars, nil
}

func (r *Runner) Deployments() (deploymentsv1connect.DeploymentServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return nil, ErrDeploymentsUnavailable
	}
	return r.client, nil
}

var ErrVarsUnavailable = errors.New("providerrunner: the variable store was reached before a successful Ready")

var ErrDeploymentsUnavailable = errors.New("providerrunner: the provider was reached before a successful Ready")

func (r *Runner) Deploy(ctx context.Context, req *deploymentsv1.DeployRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "Deploy", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.Deploy(ctx, req)
	})
}

func (r *Runner) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "Bootstrap", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.Bootstrap(ctx, req)
	})
}

func (r *Runner) Teardown(ctx context.Context, req *deploymentsv1.TeardownRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "Teardown", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.Teardown(ctx, req)
	})
}

func (r *Runner) DestroyPreview(ctx context.Context, req *deploymentsv1.DestroyPreviewRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "DestroyPreview", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.DestroyPreview(ctx, req)
	})
}

func (r *Runner) DestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "DestroyProject", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.DestroyProject(ctx, req)
	})
}

func (r *Runner) UseDomain(ctx context.Context, req *deploymentsv1.UseDomainRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "UseDomain", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.UseDomain(ctx, req)
	})
}

func (r *Runner) ReleaseDomain(ctx context.Context, req *deploymentsv1.ReleaseDomainRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "ReleaseDomain", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.ReleaseDomain(ctx, req)
	})
}

func (r *Runner) AddDomain(ctx context.Context, req *deploymentsv1.AddDomainRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "AddDomain", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.AddDomain(ctx, req)
	})
}

func (r *Runner) RemoveDomain(ctx context.Context, req *deploymentsv1.RemoveDomainRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "RemoveDomain", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.RemoveDomain(ctx, req)
	})
}

func (r *Runner) Prune(ctx context.Context, req *deploymentsv1.PruneRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	return r.stream(ctx, "Prune", onEvent, func(client deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error) {
		return client.Prune(ctx, req)
	})
}

func (r *Runner) stream(ctx context.Context, rpc string, onEvent func(*deploymentsv1.DeployEvent), call func(deploymentsv1connect.DeploymentServiceClient) (*connect.ServerStreamForClient[deploymentsv1.DeployEvent], error)) error {
	client, err := r.Deployments()
	if err != nil {
		return err
	}
	stream, callErr := call(client)
	return r.driveStream(rpc, stream, callErr, onEvent)
}

func (r *Runner) driveStream(rpc string, stream *connect.ServerStreamForClient[deploymentsv1.DeployEvent], callErr error, onEvent func(*deploymentsv1.DeployEvent)) error {
	if callErr != nil {
		return fmt.Errorf("providerrunner: call %s: %w", rpc, callErr)
	}
	defer stream.Close()

	for stream.Receive() {
		ev := stream.Msg()
		if onEvent != nil {
			onEvent(ev)
		}
		if result := ev.GetResult(); result != nil {
			if result.GetSuccess() {
				return nil
			}
			return &DeployFailedError{Message: result.GetError()}
		}
	}

	if err := stream.Err(); err != nil {
		if connect.CodeOf(err) == connect.CodeInvalidArgument {
			return fmt.Errorf("providerrunner: call %s: %w", rpc, err)
		}
		return fmt.Errorf("providerrunner: provider connection lost: %w", err)
	}
	return fmt.Errorf("providerrunner: provider closed the %s stream without a result", rpc)
}

var (
	liveMu sync.Mutex
	live   = map[*Runner]struct{}{}
)

func registerLive(r *Runner) {
	liveMu.Lock()
	live[r] = struct{}{}
	liveMu.Unlock()
}

func deregisterLive(r *Runner) {
	liveMu.Lock()
	delete(live, r)
	liveMu.Unlock()
}

func KillAllLive() {
	liveMu.Lock()
	runners := make([]*Runner, 0, len(live))
	for r := range live {
		runners = append(runners, r)
	}
	liveMu.Unlock()

	for _, r := range runners {
		_ = procgroup.Kill(r.cmd)
	}
}

func (r *Runner) Close() {
	r.closeOnce.Do(func() {
		r.teardown()

		r.mu.Lock()
		network, address := r.network, r.address
		r.mu.Unlock()

		if network == "unix" && address != "" {
			_ = os.Remove(address)
		}
	})
}

func (r *Runner) teardown() {
	if r.cmd.Process == nil {
		return
	}

	select {
	case <-r.done:
		return
	default:
		_ = procgroup.Terminate(r.cmd)
		select {
		case <-r.done:
			return
		case <-time.After(r.gracePeriod):
		}
	}

	select {
	case <-r.done:
	default:
		_ = procgroup.Kill(r.cmd)
	}

	// TODO: a pipe held open outside the group teardown owns (e.g. a
	// grandchild reparented onto init) keeps cmd.Wait() and the drain
	// goroutines blocked for the rest of the process lifetime; muting is all
	// this side can do without reaching outside the group.
	select {
	case <-r.done:
	case <-time.After(r.reapTimeout):
		r.mute()
	}
}

func (r *Runner) mute() {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	r.stdout, r.stderr = nil, nil
}

func (r *Runner) writeStdout(line string) {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	if r.stdout != nil {
		fmt.Fprintln(r.stdout, line)
	}
}

func (r *Runner) writeStderr(line string) {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	if r.stderr != nil {
		fmt.Fprintln(r.stderr, line)
	}
}

func (r *Runner) drainStdout(stdout io.Reader) {
	ready := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !ready {
			if addr, ok := channel.ParseReadinessLine(line); ok {
				ready = true
				r.readyCh <- addr
				continue
			}
		}
		r.writeStdout(line)
	}

	if err := scanner.Err(); err != nil {
		wrapped := fmt.Errorf("providerrunner: read provider stdout: %w", err)
		r.record(wrapped.Error())
		if !ready {
			select {
			case r.scanErr <- wrapped:
			default:
			}
		}
	}
}

func (r *Runner) drainStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		r.record(line)
		r.writeStderr(line)
	}

	if err := scanner.Err(); err != nil {
		r.record(fmt.Errorf("providerrunner: read provider stderr: %w", err).Error())
	}
}

func (r *Runner) record(line string) {
	r.stderrMu.Lock()
	defer r.stderrMu.Unlock()
	r.stderrBuf.WriteString(line)
	r.stderrBuf.WriteByte('\n')
}

func newSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("providerrunner: generate session token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

type authInterceptor struct {
	token string
}

func (a authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", channel.FormatAuthHeader(a.token))
		return next(ctx, req)
	}
}

func (a authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", channel.FormatAuthHeader(a.token))
		return conn
	}
}

func (a authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

type traceParentInterceptor struct{}

func (traceParentInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if traceparent, ok := channel.TraceParentFromContext(ctx); ok {
			req.Header().Set(channel.TraceParentHeader, traceparent)
		}
		return next(ctx, req)
	}
}

func (traceParentInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if traceparent, ok := channel.TraceParentFromContext(ctx); ok {
			conn.RequestHeader().Set(channel.TraceParentHeader, traceparent)
		}
		return conn
	}
}

func (traceParentInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
