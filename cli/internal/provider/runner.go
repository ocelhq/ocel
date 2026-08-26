package provider

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/cli/internal/procgroup"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/channel"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1/envvarsv1connect"
)

const DefaultReadyTimeout = 10 * time.Second

const ReadyTimeoutEnvVar = "OCEL_READY_TIMEOUT"

const DefaultGracePeriod = 2 * time.Second

const DefaultReapTimeout = 2 * time.Second

type Config struct {
	BinaryPath      string
	Args            []string
	Env             []string
	ProviderConfig  *contractv1.ProviderConfig
	ProviderPackage string
	Stdout          io.Writer
	Stderr          io.Writer
	ReadyTimeout    time.Duration
	GracePeriod     time.Duration
	ReapTimeout     time.Duration
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

type OperationFailedError struct {
	Message string
}

func (e *OperationFailedError) Error() string {
	if strings.TrimSpace(e.Message) == "" {
		return "the provider reported a failure without a reason"
	}
	return e.Message
}

type Runner struct {
	cmd             *exec.Cmd
	identity        *channel.Identity
	providerConfig  *contractv1.ProviderConfig
	providerPackage string
	stdout          io.Writer
	stderr          io.Writer
	readyTimeout    time.Duration
	gracePeriod     time.Duration
	reapTimeout     time.Duration

	readyCh chan channel.Readiness
	scanErr chan error
	done    chan struct{}
	waitErr error

	stderrMu  sync.Mutex
	stderrBuf bytes.Buffer

	outMu sync.Mutex

	mu               sync.Mutex
	network, address string
	client           contractv1connect.ProviderServiceClient
	vars             envvarsv1connect.EnvVarsServiceClient

	closeOnce sync.Once
}

func Spawn(ctx context.Context, cfg Config) (*Runner, error) {
	if cfg.BinaryPath == "" {
		return nil, errors.New("provider: BinaryPath is required")
	}

	identity, err := channel.NewIdentity()
	if err != nil {
		return nil, err
	}

	base := cfg.Env
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+1)
	env = append(env, base...)
	env = append(env, channel.ClientCertEnvVar+"="+identity.CertificatePEM())

	cmd := exec.Command(cfg.BinaryPath, cfg.Args...)
	cmd.Env = env
	procgroup.Isolate(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("provider: attach stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("provider: attach stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("provider: spawn provider %q: %w", cfg.BinaryPath, err)
	}

	r := &Runner{
		cmd:             cmd,
		identity:        identity,
		providerConfig:  cfg.ProviderConfig,
		providerPackage: cfg.ProviderPackage,
		stdout:          cfg.Stdout,
		stderr:          cfg.Stderr,
		readyTimeout:    resolveReadyTimeout(cfg.ReadyTimeout),
		gracePeriod:     resolveDuration(cfg.GracePeriod, DefaultGracePeriod),
		reapTimeout:     resolveDuration(cfg.ReapTimeout, DefaultReapTimeout),
		readyCh:         make(chan channel.Readiness, 1),
		scanErr:         make(chan error, 1),
		done:            make(chan struct{}),
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
	case ready := <-r.readyCh:
		return r.open(ctx, ready)
	case err := <-r.scanErr:
		return err
	case <-r.done:
		select {
		case ready := <-r.readyCh:
			return r.open(ctx, ready)
		default:
		}
		select {
		case err := <-r.scanErr:
			return err
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

func (r *Runner) open(ctx context.Context, ready channel.Readiness) error {
	if err := r.dial(ready); err != nil {
		return err
	}
	return r.configure(ctx)
}

func (r *Runner) configure(ctx context.Context) error {
	if r.providerConfig == nil {
		return nil
	}
	client, err := r.Client()
	if err != nil {
		return err
	}
	if _, err := client.Configure(ctx, &contractv1.ConfigureRequest{Config: r.providerConfig}); err != nil {
		var rejected *connect.Error
		if errors.As(err, &rejected) && rejected.Code() == connect.CodeInvalidArgument {
			return fmt.Errorf("%s configures %s with options it does not accept: %s", projectconfig.ConfigFileName, r.providerPackage, rejected.Message())
		}
		return fmt.Errorf("provider: configure the provider session: %w", err)
	}
	return nil
}

func (r *Runner) dial(ready channel.Readiness) error {
	network, address, err := channel.ParseAddr(ready.Addr)
	if err != nil {
		return fmt.Errorf("provider: parse readiness address: %w", err)
	}

	config, err := r.identity.ClientConfig(ready.Cert)
	if err != nil {
		return fmt.Errorf("provider: pin the provider certificate: %w", err)
	}
	httpClient := channel.HTTPClient(network, address, config)

	opts := connect.WithInterceptors(traceParentInterceptor{}, validate.NewInterceptor())

	r.mu.Lock()
	defer r.mu.Unlock()
	r.network, r.address = network, address
	r.client = contractv1connect.NewProviderServiceClient(httpClient, "https://localhost", opts)
	r.vars = envvarsv1connect.NewEnvVarsServiceClient(httpClient, "https://localhost", opts)
	return nil
}

func (r *Runner) Package() string {
	return r.providerPackage
}

func (r *Runner) Vars() (envvarsv1connect.EnvVarsServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars, nil
}

func (r *Runner) Client() (contractv1connect.ProviderServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return nil, ErrClientUnavailable
	}
	return r.client, nil
}

var ErrVarsUnavailable = errors.New("provider: the variable store was reached before a successful Ready")

var ErrClientUnavailable = errors.New("provider: the provider was reached before a successful Ready")

func Stream[Req any](
	ctx context.Context,
	r *Runner,
	rpc string,
	req *Req,
	call func(contractv1connect.ProviderServiceClient, context.Context, *Req) (*connect.ServerStreamForClient[progressv1.OperationEvent], error),
	onEvent func(*progressv1.OperationEvent),
) error {
	client, err := r.Client()
	if err != nil {
		return err
	}
	stream, callErr := call(client, ctx, req)
	return r.driveStream(rpc, stream, callErr, onEvent)
}

func (r *Runner) driveStream(rpc string, stream *connect.ServerStreamForClient[progressv1.OperationEvent], callErr error, onEvent func(*progressv1.OperationEvent)) error {
	if callErr != nil {
		return fmt.Errorf("provider: call %s: %w", rpc, callErr)
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
			return &OperationFailedError{Message: result.GetError()}
		}
	}

	if err := stream.Err(); err != nil {
		if connect.CodeOf(err) == connect.CodeInvalidArgument {
			return fmt.Errorf("provider: call %s: %w", rpc, err)
		}
		return fmt.Errorf("provider: provider connection lost: %w", err)
	}
	return fmt.Errorf("provider: provider closed the %s stream without a result", rpc)
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
			if signalled, ok := channel.ParseReadinessLine(line); ok {
				ready = true
				r.readyCh <- signalled
				continue
			}
		}
		r.writeStdout(line)
	}

	if err := scanner.Err(); err != nil {
		wrapped := fmt.Errorf("provider: read provider stdout: %w", err)
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
		r.record(fmt.Errorf("provider: read provider stderr: %w", err).Error())
	}
}

func (r *Runner) record(line string) {
	r.stderrMu.Lock()
	defer r.stderrMu.Unlock()
	r.stderrBuf.WriteString(line)
	r.stderrBuf.WriteByte('\n')
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
