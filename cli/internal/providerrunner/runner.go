// Package providerrunner owns the CLI-side lifecycle of a spawned provider
// binary: spawn it in its own process group, wait for its readiness
// sentinel (racing it against an early exit and a timeout), dial it, drive
// its Deploy RPC, and tear it down. It consumes the contracts defined in
// pkg/proto/provider/v1 (session token, readiness sentinel, Deploy
// request/stream) — see that package for the protocol itself.
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

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/v1/providerv1connect"
)

// DefaultReadyTimeout is how long Ready waits for the readiness sentinel
// when Config.ReadyTimeout is zero and ReadyTimeoutEnvVar is unset.
const DefaultReadyTimeout = 10 * time.Second

// ReadyTimeoutEnvVar overrides DefaultReadyTimeout when Config.ReadyTimeout
// is zero. Its value must parse as a time.Duration (e.g. "15s").
const ReadyTimeoutEnvVar = "OCEL_PROVIDER_READY_TIMEOUT"

// gracePeriod is how long Close waits after SIGTERM before escalating to
// SIGKILL. A var so tests can shorten it.
var gracePeriod = 5 * time.Second

// Config configures Spawn.
type Config struct {
	// BinaryPath is the provider binary to spawn.
	BinaryPath string
	// Args are extra arguments passed to BinaryPath.
	Args []string
	// Env, if non-nil, is the base environment the session token env var is
	// layered onto, instead of the inherited os.Environ(). Primarily for
	// tests.
	Env []string
	// Stdout receives every line of the provider's stdout except the
	// readiness sentinel line itself. Nil discards it.
	Stdout io.Writer
	// Stderr receives every line of the provider's stderr, in addition to
	// it being captured for EarlyExitError. Nil discards it.
	Stderr io.Writer
	// ReadyTimeout bounds how long Ready waits for the readiness sentinel.
	// Zero uses ReadyTimeoutEnvVar, falling back to DefaultReadyTimeout.
	ReadyTimeout time.Duration
}

// EarlyExitError reports that the provider process exited before printing
// the readiness sentinel.
type EarlyExitError struct {
	// Err is the error exec.Cmd.Wait returned (typically an *exec.ExitError
	// carrying the exit code).
	Err error
	// Stderr is the provider's captured stderr output, if any.
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

// ReadyTimeoutError reports that the provider never printed the readiness
// sentinel within the configured timeout.
type ReadyTimeoutError struct {
	Timeout time.Duration
}

func (e *ReadyTimeoutError) Error() string {
	return fmt.Sprintf("provider did not signal readiness within %s", e.Timeout)
}

// DeployFailedError reports that the provider's Deploy stream ended in a
// terminal ResultEvent with Success == false.
type DeployFailedError struct {
	Message string
}

func (e *DeployFailedError) Error() string {
	return "provider deploy failed: " + e.Message
}

// Runner owns a single spawned provider process for its entire lifetime:
// Spawn to launch it, Ready to wait for and dial it, Deploy to drive it, and
// Close to tear it down. All exported methods are safe to call from a
// single goroutine driving the lifecycle; Close is additionally safe to
// call concurrently (e.g. from both a deferred cleanup and a signal
// handler) and from repeat calls.
type Runner struct {
	cmd          *exec.Cmd
	token        string
	stdout       io.Writer
	stderr       io.Writer
	readyTimeout time.Duration

	readyCh chan string
	done    chan struct{}
	waitErr error

	stderrMu  sync.Mutex
	stderrBuf bytes.Buffer

	network, address string
	client           providerv1connect.ProviderServiceClient

	closeOnce sync.Once
}

// Spawn launches cfg.BinaryPath in its own process group with a fresh
// per-session token, and starts draining its stdout/stderr in the
// background. It returns as soon as the process has started; call Ready to
// wait for it to signal readiness. If ctx is cancelled (e.g. by a signal
// handler further up the call stack) before Close is called, the runner
// tears itself down the same way Close would.
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
	env = append(env, providerv1.SessionTokenEnvVar+"="+token)

	cmd := exec.Command(cfg.BinaryPath, cfg.Args...)
	cmd.Env = env
	setNewProcessGroup(cmd)

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
		stdout:       cfg.Stdout,
		stderr:       cfg.Stderr,
		readyTimeout: resolveReadyTimeout(cfg.ReadyTimeout),
		readyCh:      make(chan string, 1),
		done:         make(chan struct{}),
	}

	var drainWG sync.WaitGroup
	drainWG.Add(2)
	go func() { defer drainWG.Done(); r.drainStdout(stdoutPipe) }()
	go func() { defer drainWG.Done(); r.drainStderr(stderrPipe) }()
	go func() {
		drainWG.Wait()
		r.waitErr = cmd.Wait()
		close(r.done)
	}()

	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = r.Close()
			case <-r.done:
			}
		}()
	}

	return r, nil
}

// resolveReadyTimeout applies override, then ReadyTimeoutEnvVar, then
// DefaultReadyTimeout, in that order of precedence.
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

// Ready waits on a select of three outcomes, whichever happens first: the
// readiness sentinel appears (dialed immediately, returning nil); the
// process exits first (returns *EarlyExitError with its captured stderr,
// without waiting out the timeout); or the timeout elapses (returns
// *ReadyTimeoutError). It also returns ctx.Err() if ctx is cancelled first.
func (r *Runner) Ready(ctx context.Context) error {
	timer := time.NewTimer(r.readyTimeout)
	defer timer.Stop()

	select {
	case addr := <-r.readyCh:
		return r.dial(addr)
	case <-r.done:
		select {
		case addr := <-r.readyCh:
			return r.dial(addr)
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

// dial parses addr (as produced by the provider's readiness sentinel) and
// builds a Connect client to it, presenting the session token on every
// call.
func (r *Runner) dial(addr string) error {
	network, address, err := providerv1.ParseAddr(addr)
	if err != nil {
		return fmt.Errorf("providerrunner: parse readiness address: %w", err)
	}
	r.network = network
	r.address = address

	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, address)
			},
		},
	}

	r.client = providerv1connect.NewProviderServiceClient(
		httpClient,
		"http://provider",
		connect.WithInterceptors(authInterceptor{token: r.token}),
	)
	return nil
}

// Deploy calls the provider's Deploy RPC and streams DeployEvents to
// onEvent (which may be nil) as they arrive, including the terminal event.
// It returns nil once the stream ends in ResultEvent{Success: true}, a
// *DeployFailedError for ResultEvent{Success: false}, or an error
// describing why the stream ended without a result (e.g. the provider was
// killed mid-call). Ready must have succeeded first.
func (r *Runner) Deploy(ctx context.Context, req *providerv1.DeployRequest, onEvent func(*providerv1.DeployEvent)) error {
	if r.client == nil {
		return errors.New("providerrunner: Deploy called before a successful Ready")
	}
	stream, err := r.client.Deploy(ctx, req)
	return r.driveStream("Deploy", stream, err, onEvent)
}

// Bootstrap calls the provider's Bootstrap RPC and streams DeployEvents to
// onEvent (which may be nil) as they arrive, including the terminal event.
// Its result semantics match Deploy: nil on ResultEvent{Success: true}, a
// *DeployFailedError on failure, or a connection error. Bootstrap and Deploy
// share one event stream by contract, so they share the same driver. Ready
// must have succeeded first.
func (r *Runner) Bootstrap(ctx context.Context, req *providerv1.BootstrapRequest, onEvent func(*providerv1.DeployEvent)) error {
	if r.client == nil {
		return errors.New("providerrunner: Bootstrap called before a successful Ready")
	}
	stream, err := r.client.Bootstrap(ctx, req)
	return r.driveStream("Bootstrap", stream, err, onEvent)
}

// Destroy calls the provider's Destroy RPC and streams DeployEvents to onEvent
// (which may be nil) as they arrive, including the terminal event. Its result
// semantics match Deploy: nil on ResultEvent{Success: true}, a
// *DeployFailedError on failure, or a connection error. Destroy reuses the
// DeployEvent stream by contract, so it shares the same driver. Ready must
// have succeeded first.
func (r *Runner) Destroy(ctx context.Context, req *providerv1.DestroyRequest, onEvent func(*providerv1.DeployEvent)) error {
	if r.client == nil {
		return errors.New("providerrunner: Destroy called before a successful Ready")
	}
	stream, err := r.client.Destroy(ctx, req)
	return r.driveStream("Destroy", stream, err, onEvent)
}

// ListEnvironments calls the provider's unary ListEnvironments RPC and returns
// the enumerated preview environments. Ready must have succeeded first.
func (r *Runner) ListEnvironments(ctx context.Context, req *providerv1.ListEnvironmentsRequest) (*providerv1.ListEnvironmentsResponse, error) {
	if r.client == nil {
		return nil, errors.New("providerrunner: ListEnvironments called before a successful Ready")
	}
	resp, err := r.client.ListEnvironments(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("providerrunner: call ListEnvironments: %w", err)
	}
	return resp, nil
}

// Preflight calls the provider's unary Preflight RPC, reporting what the
// provider's ambient account/profile points at. Ready must have succeeded
// first.
func (r *Runner) Preflight(ctx context.Context, req *providerv1.PreflightRequest) (*providerv1.PreflightResponse, error) {
	if r.client == nil {
		return nil, errors.New("providerrunner: Preflight called before a successful Ready")
	}
	resp, err := r.client.Preflight(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("providerrunner: call Preflight: %w", err)
	}
	return resp, nil
}

// driveStream consumes a provider event stream to its terminal ResultEvent,
// forwarding every event to onEvent. rpc names the call for error messages.
// It is shared by Deploy, Bootstrap, and Destroy, which speak the same
// DeployEvent stream by contract.
func (r *Runner) driveStream(rpc string, stream *connect.ServerStreamForClient[providerv1.DeployEvent], callErr error, onEvent func(*providerv1.DeployEvent)) error {
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
		return fmt.Errorf("providerrunner: provider connection lost: %w", err)
	}
	return fmt.Errorf("providerrunner: provider closed the %s stream without a result", rpc)
}

// Close tears down the provider process — SIGTERM, ~5s grace, then SIGKILL
// — and removes its Unix socket file, if any. It is idempotent and safe to
// call concurrently, so it can be wired as both a deferred cleanup and a
// signal handler.
func (r *Runner) Close() error {
	r.closeOnce.Do(func() {
		r.teardown()
		if r.network == "unix" && r.address != "" {
			_ = os.Remove(r.address)
		}
	})
	return nil
}

// teardown sends SIGTERM to the process group, waits up to gracePeriod for
// it to exit, then escalates to SIGKILL.
func (r *Runner) teardown() {
	if r.cmd.Process == nil {
		return
	}

	select {
	case <-r.done:
		return
	default:
	}

	_ = terminateProcessGroup(r.cmd)

	select {
	case <-r.done:
		return
	case <-time.After(gracePeriod):
	}

	_ = killProcessGroup(r.cmd)
	<-r.done
}

// drainStdout reads the provider's stdout line by line: the first line
// matching the readiness sentinel is parsed and delivered on r.readyCh;
// every other line, before or after it, is diagnostic output forwarded to
// r.stdout.
func (r *Runner) drainStdout(stdout io.Reader) {
	ready := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !ready {
			if addr, ok := providerv1.ParseReadinessLine(line); ok {
				ready = true
				r.readyCh <- addr
				continue
			}
		}
		if r.stdout != nil {
			fmt.Fprintln(r.stdout, line)
		}
	}
}

// drainStderr reads the provider's stderr line by line, capturing it (for
// EarlyExitError) and forwarding it to r.stderr.
func (r *Runner) drainStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		r.stderrMu.Lock()
		r.stderrBuf.WriteString(line)
		r.stderrBuf.WriteByte('\n')
		r.stderrMu.Unlock()

		if r.stderr != nil {
			fmt.Fprintln(r.stderr, line)
		}
	}
}

// newSessionToken generates a fresh per-session token the CLI presents to
// the provider on every RPC call (see providerv1.SessionTokenEnvVar).
func newSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("providerrunner: generate session token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// authInterceptor presents the session token on every unary and streaming
// call, as required by the provider protocol's session token handshake.
type authInterceptor struct {
	token string
}

func (a authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", providerv1.FormatAuthHeader(a.token))
		return next(ctx, req)
	}
}

func (a authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", providerv1.FormatAuthHeader(a.token))
		return conn
	}
}

func (a authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	// The runner is only ever a client; it never serves ProviderService.
	return next
}
