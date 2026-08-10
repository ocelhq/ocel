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

	"github.com/ocelhq/ocel/pkg/channel"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
	envv1 "github.com/ocelhq/ocel/pkg/proto/env/v1"
	"github.com/ocelhq/ocel/pkg/proto/env/v1/envv1connect"
)

const DefaultReadyTimeout = 10 * time.Second

const ReadyTimeoutEnvVar = "OCEL_READY_TIMEOUT"

var gracePeriod = 5 * time.Second

type Config struct {
	BinaryPath   string
	Args         []string
	Env          []string
	Stdout       io.Writer
	Stderr       io.Writer
	ReadyTimeout time.Duration
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
	stdout       io.Writer
	stderr       io.Writer
	readyTimeout time.Duration

	readyCh chan string
	done    chan struct{}
	waitErr error

	stderrMu  sync.Mutex
	stderrBuf bytes.Buffer

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

func (r *Runner) dial(addr string) error {
	network, address, err := channel.ParseAddr(addr)
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

	auth := connect.WithInterceptors(authInterceptor{token: r.token})
	r.client = deploymentsv1connect.NewDeploymentServiceClient(httpClient, "http://provider", auth)
	r.vars = envv1connect.NewEnvVarsServiceClient(httpClient, "http://provider", auth)
	return nil
}

var ErrVarsUnavailable = errors.New("providerrunner: the variable store was reached before a successful Ready")

func (r *Runner) SetValue(ctx context.Context, req *envv1.SetValueRequest) (*envv1.SetValueResponse, error) {
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars.SetValue(ctx, req)
}

func (r *Runner) ListValues(ctx context.Context, req *envv1.ListValuesRequest) (*envv1.ListValuesResponse, error) {
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars.ListValues(ctx, req)
}

func (r *Runner) GetValue(ctx context.Context, req *envv1.GetValueRequest) (*envv1.GetValueResponse, error) {
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars.GetValue(ctx, req)
}

func (r *Runner) RevealValues(ctx context.Context, req *envv1.RevealValuesRequest) (*envv1.RevealValuesResponse, error) {
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars.RevealValues(ctx, req)
}

func (r *Runner) DeleteValue(ctx context.Context, req *envv1.DeleteValueRequest) (*envv1.DeleteValueResponse, error) {
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars.DeleteValue(ctx, req)
}

func (r *Runner) SetReference(ctx context.Context, req *envv1.SetReferenceRequest) (*envv1.SetReferenceResponse, error) {
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars.SetReference(ctx, req)
}

func (r *Runner) ListReferences(ctx context.Context, req *envv1.ListReferencesRequest) (*envv1.ListReferencesResponse, error) {
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars.ListReferences(ctx, req)
}

func (r *Runner) ListVersions(ctx context.Context, req *envv1.ListVersionsRequest) (*envv1.ListVersionsResponse, error) {
	if r.vars == nil {
		return nil, ErrVarsUnavailable
	}
	return r.vars.ListVersions(ctx, req)
}

func (r *Runner) Deploy(ctx context.Context, req *deploymentsv1.DeployRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	if r.client == nil {
		return errors.New("providerrunner: Deploy called before a successful Ready")
	}
	stream, err := r.client.Deploy(ctx, req)
	return r.driveStream("Deploy", stream, err, onEvent)
}

func (r *Runner) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	if r.client == nil {
		return errors.New("providerrunner: Bootstrap called before a successful Ready")
	}
	stream, err := r.client.Bootstrap(ctx, req)
	return r.driveStream("Bootstrap", stream, err, onEvent)
}

func (r *Runner) DestroyPreview(ctx context.Context, req *deploymentsv1.DestroyPreviewRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	if r.client == nil {
		return errors.New("providerrunner: DestroyPreview called before a successful Ready")
	}
	stream, err := r.client.DestroyPreview(ctx, req)
	return r.driveStream("DestroyPreview", stream, err, onEvent)
}

func (r *Runner) DestroyProject(ctx context.Context, req *deploymentsv1.DestroyProjectRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	if r.client == nil {
		return errors.New("providerrunner: DestroyProject called before a successful Ready")
	}
	stream, err := r.client.DestroyProject(ctx, req)
	return r.driveStream("DestroyProject", stream, err, onEvent)
}

func (r *Runner) PlanDestroyProject(ctx context.Context, req *deploymentsv1.PlanDestroyProjectRequest) (*deploymentsv1.PlanDestroyProjectResponse, error) {
	if r.client == nil {
		return nil, errors.New("providerrunner: PlanDestroyProject called before a successful Ready")
	}
	resp, err := r.client.PlanDestroyProject(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("providerrunner: call PlanDestroyProject: %w", err)
	}
	return resp, nil
}

func (r *Runner) ListEnvironments(ctx context.Context, req *deploymentsv1.ListEnvironmentsRequest) (*deploymentsv1.ListEnvironmentsResponse, error) {
	if r.client == nil {
		return nil, errors.New("providerrunner: ListEnvironments called before a successful Ready")
	}
	resp, err := r.client.ListEnvironments(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("providerrunner: call ListEnvironments: %w", err)
	}
	return resp, nil
}

func (r *Runner) Preflight(ctx context.Context, req *deploymentsv1.PreflightRequest) (*deploymentsv1.PreflightResponse, error) {
	if r.client == nil {
		return nil, errors.New("providerrunner: Preflight called before a successful Ready")
	}
	resp, err := r.client.Preflight(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("providerrunner: call Preflight: %w", err)
	}
	return resp, nil
}

func (r *Runner) ListPromotions(ctx context.Context, req *deploymentsv1.ListPromotionsRequest) (*deploymentsv1.ListPromotionsResponse, error) {
	if r.client == nil {
		return nil, errors.New("providerrunner: ListPromotions called before a successful Ready")
	}
	resp, err := r.client.ListPromotions(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("providerrunner: call ListPromotions: %w", err)
	}
	return resp, nil
}

func (r *Runner) Rollback(ctx context.Context, req *deploymentsv1.RollbackRequest) (*deploymentsv1.RollbackResponse, error) {
	if r.client == nil {
		return nil, errors.New("providerrunner: Rollback called before a successful Ready")
	}
	resp, err := r.client.Rollback(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("providerrunner: call Rollback: %w", err)
	}
	return resp, nil
}

func (r *Runner) Prune(ctx context.Context, req *deploymentsv1.PruneRequest, onEvent func(*deploymentsv1.DeployEvent)) error {
	if r.client == nil {
		return errors.New("providerrunner: Prune called before a successful Ready")
	}
	stream, err := r.client.Prune(ctx, req)
	return r.driveStream("Prune", stream, err, onEvent)
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
		return fmt.Errorf("providerrunner: provider connection lost: %w", err)
	}
	return fmt.Errorf("providerrunner: provider closed the %s stream without a result", rpc)
}

func (r *Runner) Close() error {
	r.closeOnce.Do(func() {
		r.teardown()
		if r.network == "unix" && r.address != "" {
			_ = os.Remove(r.address)
		}
	})
	return nil
}

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
		if r.stdout != nil {
			fmt.Fprintln(r.stdout, line)
		}
	}
}

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
