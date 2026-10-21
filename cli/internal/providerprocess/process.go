package providerprocess

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/cli/internal/wireerror"
	"github.com/ocelhq/ocel/pkg/localrpc"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
)

const DefaultReadyTimeout = 10 * time.Second

const ReadyTimeoutEnvVar = "OCEL_READY_TIMEOUT"

const DefaultGracePeriod = 10 * time.Second

const DefaultReapTimeout = 2 * time.Second

const MaxMessageBytes = 128 << 20

type LaunchSpec struct {
	BinaryPath      string
	Args            []string
	Env             []string
	ProviderConfig  *contractv1.ProviderConfig
	Edge            *contractv1.EdgeSelection
	ProviderName    string
	Stdout          io.Writer
	Stderr          io.Writer
	ReadyTimeout    time.Duration
	GracePeriod     time.Duration
	ReapTimeout     time.Duration
	MaxMessageBytes int
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

type VersionMismatchError struct {
	Name      string
	Announced string
	Expected  string
}

func (e *VersionMismatchError) Error() string {
	named := "the provider"
	if e.Name != "" {
		named = "provider " + e.Name
	}
	return fmt.Sprintf("%s is version %s and this CLI is version %s; a provider ships with the CLI it runs under", named, e.Announced, e.Expected)
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

type Process struct {
	cmd             *exec.Cmd
	child           *childprocess.Child
	stdin           io.Closer
	identity        *localrpc.Identity
	providerConfig  *contractv1.ProviderConfig
	edge            *contractv1.EdgeSelection
	providerName    string
	stdout          io.Writer
	stderr          io.Writer
	readyTimeout    time.Duration
	reapTimeout     time.Duration
	maxMessageBytes int

	readyCh chan localrpc.Readiness
	scanErr chan error
	done    chan struct{}
	waitErr error

	stderrMu  sync.Mutex
	stderrBuf bytes.Buffer

	outMu sync.Mutex

	mu               sync.Mutex
	network, address string
	clients          clients
	facts            *contractv1.ProviderFacts

	closeOnce sync.Once
}

func Spawn(spec LaunchSpec) (*Process, error) {
	if spec.BinaryPath == "" {
		return nil, errors.New("provider: BinaryPath is required")
	}

	identity, err := localrpc.NewIdentity()
	if err != nil {
		return nil, err
	}

	base := spec.Env
	if base == nil {
		base = os.Environ()
	}
	env := make([]string, 0, len(base)+1)
	env = append(env, base...)
	env = append(env, localrpc.ClientCertEnvVar+"="+identity.CertificatePEM())

	cmd := exec.Command(spec.BinaryPath, spec.Args...)
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("provider: attach stdin pipe: %w", err)
	}
	stdoutPipe, stdoutWriter := io.Pipe()
	stderrPipe, stderrWriter := io.Pipe()
	cmd.Stdout, cmd.Stderr = stdoutWriter, stderrWriter

	gracePeriod := resolveDuration(spec.GracePeriod, DefaultGracePeriod)
	reapTimeout := resolveDuration(spec.ReapTimeout, DefaultReapTimeout)
	child, err := childprocess.Start(context.Background(), cmd, childprocess.Options{Grace: gracePeriod, Reap: reapTimeout})
	if err != nil {
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		return nil, &clierror.Error{Code: clierror.CodeProviderUnavailable, Cause: fmt.Errorf("provider: spawn provider %q: %w", spec.BinaryPath, err)}
	}

	r := &Process{
		cmd:             cmd,
		child:           child,
		stdin:           stdin,
		identity:        identity,
		providerConfig:  spec.ProviderConfig,
		edge:            spec.Edge,
		providerName:    spec.ProviderName,
		stdout:          spec.Stdout,
		stderr:          spec.Stderr,
		readyTimeout:    resolveReadyTimeout(spec.ReadyTimeout),
		reapTimeout:     reapTimeout,
		maxMessageBytes: resolveBytes(spec.MaxMessageBytes, MaxMessageBytes),
		readyCh:         make(chan localrpc.Readiness, 1),
		scanErr:         make(chan error, 1),
		done:            make(chan struct{}),
	}

	var drainWG sync.WaitGroup
	drainWG.Add(2)
	go func() { defer drainWG.Done(); r.drainStdout(stdoutPipe) }()
	go func() { defer drainWG.Done(); r.drainStderr(stderrPipe) }()
	go func() {
		r.waitErr = child.Wait()
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		drainWG.Wait()
		close(r.done)
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

func resolveBytes(override, def int) int {
	if override > 0 {
		return override
	}
	return def
}

func (r *Process) Ready(ctx context.Context) error {
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
		return &clierror.Error{Code: clierror.CodeProviderUnavailable, Cause: &EarlyExitError{Err: r.waitErr, Stderr: stderr}}
	case <-timer.C:
		return &clierror.Error{Code: clierror.CodeProviderUnavailable, Cause: &ReadyTimeoutError{Timeout: r.readyTimeout}}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Process) open(ctx context.Context, ready localrpc.Readiness) error {
	if ready.Version != version.Version {
		return &clierror.Error{Code: clierror.CodeProviderVersionMismatch, Cause: &VersionMismatchError{Name: r.providerName, Announced: ready.Version, Expected: version.Version}}
	}
	if err := r.dial(ready); err != nil {
		return &clierror.Error{Code: clierror.CodeProviderUnavailable, Cause: err}
	}
	return r.configure(ctx)
}

func (r *Process) configure(ctx context.Context) error {
	if r.providerConfig == nil {
		return nil
	}
	client, err := r.Client()
	if err != nil {
		return err
	}
	facts, err := configure(ctx, client, r.providerName, r.providerConfig, r.edge)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.facts = facts
	r.mu.Unlock()
	return nil
}

func (r *Process) Facts() *contractv1.ProviderFacts {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.facts
}

func (r *Process) dial(ready localrpc.Readiness) error {
	network, address, err := localrpc.ParseAddress(ready.Address)
	if err != nil {
		return fmt.Errorf("provider: parse readiness address: %w", err)
	}

	config, err := r.identity.ClientConfig(ready.Cert)
	if err != nil {
		return fmt.Errorf("provider: pin the provider certificate: %w", err)
	}
	httpClient := localrpc.HTTPClient(network, address, config)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.network, r.address = network, address
	r.clients = newClients(httpClient, "https://localhost", r.maxMessageBytes, wireerror.Interceptor(providerRefusal))
	return nil
}

func (r *Process) Name() string {
	return r.providerName
}

func (r *Process) Cost() (costv1connect.CostServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clients.cost == nil {
		return nil, ErrClientUnavailable
	}
	return r.clients.cost, nil
}

func (r *Process) VariableStore() (variablestorev1connect.VariableStoreServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clients.variableStore == nil {
		return nil, ErrVariableStoreUnavailable
	}
	return r.clients.variableStore, nil
}

func (r *Process) Client() (contractv1connect.ProviderServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clients.provider == nil {
		return nil, ErrClientUnavailable
	}
	return r.clients.provider, nil
}

var ErrVariableStoreUnavailable = errors.New("provider: the variable store was reached before a successful Ready")

var ErrClientUnavailable = errors.New("provider: the provider was reached before a successful Ready")

const (
	exitGrace      = 500 * time.Millisecond
	exitStderrTail = 20
)

func (r *Process) explainLost(err error) error {
	select {
	case <-r.done:
	case <-time.After(exitGrace):
		return err
	}
	r.stderrMu.Lock()
	lines := strings.Split(strings.TrimSpace(r.stderrBuf.String()), "\n")
	r.stderrMu.Unlock()
	if len(lines) > exitStderrTail {
		lines = lines[len(lines)-exitStderrTail:]
	}
	if tail := strings.Join(lines, "\n"); tail != "" {
		return fmt.Errorf("%w\n%s", err, tail)
	}
	return err
}

func (r *Process) Close() {
	r.closeOnce.Do(func() {
		r.teardown()

		r.mu.Lock()
		network, address := r.network, r.address
		r.mu.Unlock()

		if network == "unix" && address != "" {
			_ = os.Remove(address)
			_ = os.Remove(filepath.Dir(address))
		}
	})
}

func (r *Process) teardown() {
	_ = r.stdin.Close()
	r.child.Stop()
	select {
	case <-r.done:
	case <-time.After(r.reapTimeout):
		r.mute()
	}
}

func (r *Process) mute() {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	r.stdout, r.stderr = nil, nil
}

func (r *Process) writeStdout(line string) {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	if r.stdout != nil {
		fmt.Fprintln(r.stdout, line)
	}
}

func (r *Process) writeStderr(line string) {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	if r.stderr != nil {
		fmt.Fprintln(r.stderr, line)
	}
}

func (r *Process) drainStdout(stdout io.Reader) {
	ready := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !ready {
			if signalled, ok := localrpc.ParseReadinessLine(line); ok {
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
			case r.scanErr <- &clierror.Error{Code: clierror.CodeProviderUnavailable, Cause: wrapped}:
			default:
			}
		}
	}
}

func (r *Process) drainStderr(stderr io.Reader) {
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

func (r *Process) record(line string) {
	r.stderrMu.Lock()
	defer r.stderrMu.Unlock()
	r.stderrBuf.WriteString(line)
	r.stderrBuf.WriteByte('\n')
}

type traceParentInterceptor struct{}

func (traceParentInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if traceparent, ok := localrpc.TraceParentFromContext(ctx); ok {
			req.Header().Set(localrpc.TraceParentHeader, traceparent)
		}
		return next(ctx, req)
	}
}

func (traceParentInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if traceparent, ok := localrpc.TraceParentFromContext(ctx); ok {
			conn.RequestHeader().Set(localrpc.TraceParentHeader, traceparent)
		}
		return conn
	}
}

func (traceParentInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
