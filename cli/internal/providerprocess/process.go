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
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/localrpc"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const DefaultReadyTimeout = 10 * time.Second

const ReadyTimeoutEnvVar = "OCEL_READY_TIMEOUT"

const DefaultGracePeriod = 2 * time.Second

const DefaultReapTimeout = 2 * time.Second

const MaxMessageBytes = 128 << 20

type LaunchSpec struct {
	BinaryPath      string
	Args            []string
	Env             []string
	ProviderConfig  *contractv1.ProviderConfig
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
	identity        *localrpc.Identity
	providerConfig  *contractv1.ProviderConfig
	providerName    string
	stdout          io.Writer
	stderr          io.Writer
	readyTimeout    time.Duration
	gracePeriod     time.Duration
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
	client           contractv1connect.ProviderServiceClient
	variableStore    variablestorev1connect.VariableStoreServiceClient
	cost             costv1connect.CostServiceClient
	facts            *contractv1.ProviderFacts

	closeOnce sync.Once
}

func Spawn(ctx context.Context, spec LaunchSpec) (*Process, error) {
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
	childprocess.SetOwnGroup(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("provider: attach stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("provider: attach stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("provider: spawn provider %q: %w", spec.BinaryPath, err)
	}

	r := &Process{
		cmd:             cmd,
		identity:        identity,
		providerConfig:  spec.ProviderConfig,
		providerName:    spec.ProviderName,
		stdout:          spec.Stdout,
		stderr:          spec.Stderr,
		readyTimeout:    resolveReadyTimeout(spec.ReadyTimeout),
		gracePeriod:     resolveDuration(spec.GracePeriod, DefaultGracePeriod),
		reapTimeout:     resolveDuration(spec.ReapTimeout, DefaultReapTimeout),
		maxMessageBytes: resolveBytes(spec.MaxMessageBytes, MaxMessageBytes),
		readyCh:         make(chan localrpc.Readiness, 1),
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
		_ = childprocess.KillGroup(cmd)
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
		return &EarlyExitError{Err: r.waitErr, Stderr: stderr}
	case <-timer.C:
		return &ReadyTimeoutError{Timeout: r.readyTimeout}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Process) open(ctx context.Context, ready localrpc.Readiness) error {
	if ready.Version != version.Version {
		return &VersionMismatchError{Name: r.providerName, Announced: ready.Version, Expected: version.Version}
	}
	if err := r.dial(ready); err != nil {
		return err
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
	configured, err := client.Configure(ctx, &contractv1.ConfigureRequest{Config: r.providerConfig})
	if err != nil {
		var rejected *connect.Error
		if errors.As(err, &rejected) && rejected.Code() == connect.CodeInvalidArgument {
			if code, named := provider.RefusedCode(err); named && code == refusal.CodeUnknownOption {
				return fmt.Errorf("the config configures provider %q with options it does not accept: %s", r.providerName, rejected.Message())
			}
			return fmt.Errorf("provider %q refuses the config it was given: %s", r.providerName, rejected.Message())
		}
		return fmt.Errorf("provider: configure the provider session: %w", err)
	}
	r.mu.Lock()
	r.facts = configured.GetFacts()
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

	opts := connect.WithClientOptions(
		connect.WithInterceptors(traceParentInterceptor{}, validate.NewInterceptor()),
		connect.WithReadMaxBytes(r.maxMessageBytes),
	)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.network, r.address = network, address
	r.client = contractv1connect.NewProviderServiceClient(httpClient, "https://localhost", opts)
	r.variableStore = variablestorev1connect.NewVariableStoreServiceClient(httpClient, "https://localhost", opts)
	r.cost = costv1connect.NewCostServiceClient(httpClient, "https://localhost", opts)
	return nil
}

func (r *Process) Name() string {
	return r.providerName
}

func (r *Process) Cost() (costv1connect.CostServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cost == nil {
		return nil, ErrClientUnavailable
	}
	return r.cost, nil
}

func (r *Process) VariableStore() (variablestorev1connect.VariableStoreServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.variableStore == nil {
		return nil, ErrVariableStoreUnavailable
	}
	return r.variableStore, nil
}

func (r *Process) Client() (contractv1connect.ProviderServiceClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return nil, ErrClientUnavailable
	}
	return r.client, nil
}

var ErrVariableStoreUnavailable = errors.New("provider: the variable store was reached before a successful Ready")

var ErrClientUnavailable = errors.New("provider: the provider was reached before a successful Ready")

type streamCall[Req any] func(contractv1connect.ProviderServiceClient, context.Context, *Req) (*connect.ServerStreamForClient[progressv1.OperationEvent], error)

func stream[Req any](ctx context.Context, r *Process, rpc string, req *Req, call streamCall[Req], onEvent func(*progressv1.OperationEvent)) (*progressv1.OperationResult, error) {
	client, err := r.Client()
	if err != nil {
		return nil, err
	}
	events, callErr := call(client, ctx, req)
	return r.driveStream(rpc, events, callErr, onEvent)
}

func (r *Process) driveStream(rpc string, stream *connect.ServerStreamForClient[progressv1.OperationEvent], callErr error, onEvent func(*progressv1.OperationEvent)) (*progressv1.OperationResult, error) {
	if callErr != nil {
		return nil, r.callError(rpc, callErr)
	}
	defer stream.Close()

	refused := false
	for stream.Receive() {
		event := stream.Msg()
		if onEvent != nil {
			onEvent(event)
		}
		result := event.GetResult()
		refused = refused || result.GetRefused()
		if result != nil && !result.GetRefused() {
			if result.GetSuccess() {
				return result, nil
			}
			return result, &OperationFailedError{Message: result.GetError()}
		}
	}

	if err := stream.Err(); err != nil {
		return nil, r.streamError(rpc, err, refused)
	}
	return nil, r.withExitStderr(fmt.Errorf("provider: provider closed the %s stream without a result", rpc))
}

func (r *Process) callError(rpc string, err error) error {
	if cancelled(err) {
		return fmt.Errorf("provider: %s was cancelled: %w", rpc, err)
	}
	return r.withExitStderr(fmt.Errorf("provider: call %s: %w", rpc, err))
}

func (r *Process) streamError(rpc string, err error, refused bool) error {
	if cancelled(err) {
		return fmt.Errorf("provider: %s was cancelled: %w", rpc, err)
	}
	if _, named := provider.RefusedCode(err); refused || named {
		return fmt.Errorf("provider: call %s: %w", rpc, err)
	}
	if connect.CodeOf(err) == connect.CodeInvalidArgument {
		return r.withExitStderr(fmt.Errorf("provider: call %s: %w", rpc, err))
	}
	return r.withExitStderr(fmt.Errorf("provider: provider connection lost: %w", err))
}

func (r *Process) readLogs(ctx context.Context, req *contractv1.ReadLogsRequest, onResponse func(*contractv1.ReadLogsResponse) error) error {
	client, err := r.Client()
	if err != nil {
		return err
	}
	stream, err := client.ReadLogs(ctx, req)
	if err != nil {
		return r.callError("ReadLogs", err)
	}
	defer stream.Close()

	for stream.Receive() {
		if err := onResponse(stream.Msg()); err != nil {
			return err
		}
	}
	if err := stream.Err(); err != nil {
		return r.streamError("ReadLogs", err, false)
	}
	return nil
}

const (
	exitGrace      = 500 * time.Millisecond
	exitStderrTail = 20
)

func (r *Process) withExitStderr(err error) error {
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

func cancelled(err error) bool {
	return errors.Is(err, context.Canceled) || connect.CodeOf(err) == connect.CodeCanceled
}

var (
	liveMu sync.Mutex
	live   = map[*Process]struct{}{}
)

func registerLive(r *Process) {
	liveMu.Lock()
	live[r] = struct{}{}
	liveMu.Unlock()
}

func deregisterLive(r *Process) {
	liveMu.Lock()
	delete(live, r)
	liveMu.Unlock()
}

func KillAllLive() {
	liveMu.Lock()
	processes := make([]*Process, 0, len(live))
	for r := range live {
		processes = append(processes, r)
	}
	liveMu.Unlock()

	for _, r := range processes {
		_ = childprocess.KillGroup(r.cmd)
	}
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
	if r.cmd.Process == nil {
		return
	}

	select {
	case <-r.done:
		return
	default:
		_ = childprocess.TerminateGroup(r.cmd)
		select {
		case <-r.done:
			return
		case <-time.After(r.gracePeriod):
		}
	}

	select {
	case <-r.done:
	default:
		_ = childprocess.KillGroup(r.cmd)
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
			case r.scanErr <- wrapped:
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
