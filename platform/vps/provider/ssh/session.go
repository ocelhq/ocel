package ssh

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	connectTimeout = 10 * time.Second
	aliveInterval  = 15 * time.Second
	aliveProbes    = 4
	masterIdle     = "60s"

	concurrentCommandLimit = 8

	scope = "github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type Session struct {
	target  Target
	dest    Destination
	anchor  HostKey
	control string

	commandSlotsMade sync.Once
	commandSlots     chan struct{}

	spansMade sync.Once
	spans     *spanForward

	openedUnder trace.SpanContext
}

func Open(ctx context.Context, target Target) (_ *Session, err error) {
	openedUnder := trace.SpanContextFromContext(ctx)
	ctx, span := otel.Tracer(scope).Start(ctx, "open ssh session",
		trace.WithAttributes(attribute.String("ssh.destination", target.Destination())))
	defer func() { tracing.EndSpan(span, err) }()

	dest, err := resolve(ctx, target)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"ssh cannot make sense of %q: %s", target.Destination(), terse(err))
	}
	anchor, err := anchorHostKey(ctx, dest)
	if err != nil {
		return nil, err
	}
	session := &Session{target: target, dest: dest, anchor: anchor, control: multiplex(), openedUnder: openedUnder}
	if err = session.establishMaster(ctx); err != nil {
		session.Close()
		return nil, err
	}
	return session, nil
}

func anchorHostKey(ctx context.Context, dest Destination) (_ HostKey, err error) {
	ctx, span := otel.Tracer(scope).Start(ctx, "check ssh host key")
	defer func() { tracing.EndSpan(span, err) }()

	started := time.Now()
	keys, err := offered(ctx, dest)
	if err != nil {
		return HostKey{}, refuseUnanswered(ctx, dest, err, started.Add(connectTimeout))
	}
	offers := make([]string, 0, len(keys))
	for _, key := range keys {
		offers = append(offers, key.Type+" "+key.Fingerprint)
	}
	span.SetAttributes(attribute.StringSlice("ssh.host_key.offered", offers))
	if len(keys) == 0 {
		return HostKey{}, refusal.Refuse(refusal.CodeNotReady,
			"%s port %d offered no ssh host key", dest.Address, dest.Port)
	}
	listed := recorded(ctx, dest)
	span.SetAttributes(
		attribute.Int("ssh.host_key.recorded", len(listed.keys)),
		attribute.Bool("ssh.host_key.delegated", listed.delegated),
	)
	anchor, trust := classify(dest, keys, listed)
	if trust != nil {
		span.SetAttributes(attribute.String("ssh.host_key.trust", string(trust.Reason)))
		return HostKey{}, RefuseHostTrust(*trust)
	}
	span.SetAttributes(
		attribute.String("ssh.host_key.trust", "trusted"),
		attribute.String("ssh.host_key.fingerprint", anchor.Fingerprint),
	)
	return anchor, nil
}

func (s *Session) establishMaster(ctx context.Context) (err error) {
	ctx, span := otel.Tracer(scope).Start(ctx, "start ssh master",
		trace.WithAttributes(attribute.String("ssh.control_path", s.control)))
	defer func() { tracing.EndSpan(span, err) }()
	_, err = s.Run(ctx, "true")
	return err
}

func refuseUnanswered(ctx context.Context, dest Destination, scanned error, by time.Time) error {
	var said failure
	if errors.As(scanned, &said) && said.stderr != "" {
		return refusal.Refuse(refusal.CodeNotReady,
			"%s port %d is unreachable: %s", dest.Address, dest.Port, terse(scanned))
	}
	dialCtx, cancel := context.WithDeadline(ctx, by)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", net.JoinHostPort(dest.Address, strconv.Itoa(dest.Port)))
	switch {
	case err == nil:
		conn.Close()
		return refusal.Refuse(refusal.CodeNotReady,
			"%s port %d accepted the connection but offered no ssh host key", dest.Address, dest.Port)
	case isRefused(err):
		return refusal.Refuse(refusal.CodeNotReady,
			"%s port %d refused the connection: nothing accepts ssh there", dest.Address, dest.Port)
	case errors.Is(err, context.DeadlineExceeded):
		return refusal.Refuse(refusal.CodeNotReady,
			"%s port %d did not answer within %s", dest.Address, dest.Port, connectTimeout)
	default:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s port %d is unreachable: %s", dest.Address, dest.Port, terse(err))
	}
}

const wsaConnectionRefused = syscall.Errno(10061)

func isRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, wsaConnectionRefused)
}

func (s *Session) HostKey() HostKey { return s.anchor }

func (s *Session) Destination() Destination { return s.dest }

type Result struct {
	Stdout string
	Stderr string
	Code   int

	sudo       bool
	unelevated error
}

func (s *Session) Run(ctx context.Context, command string) (string, error) {
	result, err := s.Stream(ctx, command, nil)
	if err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", s.refuseNonZeroExit(command, result)
	}
	return result.Stdout, nil
}

type Pipe int

const (
	Stdout Pipe = iota
	Stderr
)

type Line struct {
	Pipe Pipe
	Text string
}

const (
	longestLineBytes = 1 << 20
	keptStderrBytes  = 64 << 10
)

func (s *Session) RunLines(ctx context.Context, command string, each func(Line) error) error {
	release, waited, err := s.takeCommandSlot(ctx)
	if err != nil {
		return err
	}
	defer release()

	commandCtx, stop := context.WithCancel(ctx)
	defer stop()

	cmd := s.newSSHCommand(commandCtx, command, waited)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return s.refuseUnreached(ctx, err, "")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return s.refuseUnreached(ctx, err, "")
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		return s.refuseUnreached(ctx, err, "")
	}
	defer input.Close()
	cmd.Cancel = func() error {
		input.Close()
		killErr := cmd.Process.Kill()
		stdout.Close()
		stderr.Close()
		return killErr
	}
	if err := cmd.Start(); err != nil {
		return s.refuseUnreached(ctx, err, "")
	}

	var (
		mu          sync.Mutex
		callbackErr error
		readErr     error
		keptStderr  = tracing.Tail{Limit: keptStderrBytes}
	)
	deliver := func(line Line) bool {
		mu.Lock()
		defer mu.Unlock()
		if callbackErr != nil || readErr != nil {
			return false
		}
		if line.Pipe == Stderr {
			_, _ = io.WriteString(&keptStderr, line.Text+"\n")
		}
		if err := each(line); err != nil {
			callbackErr = err
			stop()
			return false
		}
		return true
	}
	var readers sync.WaitGroup
	for pipe, reader := range map[Pipe]io.Reader{Stdout: stdout, Stderr: stderr} {
		readers.Go(func() {
			err := readLines(reader, func(text string) bool { return deliver(Line{Pipe: pipe, Text: text}) })
			if err == nil || errors.Is(err, os.ErrClosed) {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if readErr == nil {
				readErr = err
				stop()
			}
		})
	}
	readers.Wait()
	waitErr := cmd.Wait()
	kept := strings.TrimSpace(keptStderr.WholeLines())
	code, err := exitStatus(waitErr)

	switch {
	case callbackErr != nil:
		return callbackErr
	case readErr != nil:
		return refusal.Refuse(refusal.CodeNotReady,
			"%s over ssh: the output of %s is unreadable: %s", s.dest.Principal(), command, terse(readErr))
	case cmd.ProcessState != nil && cmd.ProcessState.Success():
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	}
	if err == nil && code != transportFailure {
		return s.refuseNonZeroExit(command, Result{Stderr: kept, Code: code})
	}
	return s.refuseUnreached(ctx, err, kept)
}

func readLines(pipe io.Reader, deliver func(string) bool) error {
	reader := bufio.NewReaderSize(pipe, longestLineBytes)
	for {
		text, err := reader.ReadSlice('\n')
		if len(text) > 0 && !deliver(strings.TrimSuffix(strings.TrimSuffix(string(text), "\n"), "\r")) {
			return nil
		}
		for errors.Is(err, bufio.ErrBufferFull) {
			_, err = reader.ReadSlice('\n')
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (s *Session) Stream(ctx context.Context, command string, stdin io.Reader) (Result, error) {
	release, waited, err := s.takeCommandSlot(ctx)
	if err != nil {
		return Result{}, err
	}
	defer release()
	stdout, stderr, code, err := collectOutput(s.newSSHCommand(ctx, command, waited), stdin)
	if err == nil && code != transportFailure {
		return Result{Stdout: stdout, Stderr: stderr, Code: code}, nil
	}
	return Result{}, s.refuseUnreached(ctx, err, stderr)
}

func (s *Session) takeCommandSlot(ctx context.Context) (func(), time.Duration, error) {
	s.commandSlotsMade.Do(func() { s.commandSlots = make(chan struct{}, concurrentCommandLimit) })
	asked := time.Now()
	select {
	case s.commandSlots <- struct{}{}:
		return func() { <-s.commandSlots }, time.Since(asked), nil
	case <-ctx.Done():
		return nil, time.Since(asked), ctx.Err()
	}
}

func (s *Session) newSSHCommand(ctx context.Context, command string, waited time.Duration) *tracing.Cmd {
	cmd := tracing.CommandContext(ctx, "ssh", append(s.args(), s.dest.Written, command)...)
	cmd.SetAttributes(
		attribute.String("ssh.principal", s.dest.Principal()),
		attribute.Int64("ssh.command.slot_wait_ms", waited.Milliseconds()),
	)
	return cmd
}

func (s *Session) refuseNonZeroExit(command string, result Result) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"%s over ssh: %s", s.dest.Principal(), terse(result.problem(command)))
}

func (s *Session) refuseUnreached(ctx context.Context, err error, stderr string) error {
	if strings.Contains(stderr, "Host key verification failed") {
		keys, _ := offered(ctx, s.dest)
		if _, trust := classify(s.dest, keys, recorded(ctx, s.dest)); trust != nil {
			return RefuseHostTrust(*trust)
		}
	}
	unreached := refusal.CodeNotReady
	if loginRefused(stderr) {
		unreached = refusal.CodeDenied
	}
	return refusal.Refuse(unreached,
		"%s over ssh: %s", s.dest.Principal(), terse(failure{err: err, stderr: stderr}))
}

var loginRefusals = []string{"Permission denied (", "Too many authentication failures"}

func loginRefused(stderr string) bool {
	return slices.ContainsFunc(loginRefusals, func(message string) bool { return strings.Contains(stderr, message) })
}

func (r Result) problem(command string) error {
	if r.Stderr != "" {
		return errors.New(r.Stderr)
	}
	return fmt.Errorf("%s exited %d", command, r.Code)
}

const transportFailure = 255

func (s *Session) Close() (err error) {
	if s.control == "" {
		return nil
	}
	ctx, span := otel.Tracer(scope).Start(trace.ContextWithSpanContext(context.Background(), s.openedUnder), "close ssh session",
		trace.WithAttributes(attribute.String("ssh.control_path", s.control)))
	defer func() { tracing.EndSpan(span, err) }()
	s.closeSpanForward()
	_, err = output(ctx, "ssh", append(s.args(), "-O", "exit", s.dest.Written)...)
	if err != nil {
		if _, stat := os.Stat(s.control); errors.Is(stat, os.ErrNotExist) {
			return nil
		}
	}
	return err
}

func (s *Session) args() []string {
	args := append(s.target.options(),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout="+strconv.Itoa(int(connectTimeout.Seconds())),
		"-o", "ServerAliveInterval="+strconv.Itoa(int(aliveInterval.Seconds())),
		"-o", "ServerAliveCountMax="+strconv.Itoa(aliveProbes),
	)
	if s.control != "" {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+s.control,
			"-o", "ControlPersist="+masterIdle,
		)
	}
	return args
}

func multiplex() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(cache, "ocel", "ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return filepath.Join(dir, strconv.Itoa(os.Getpid())+"-"+strconv.FormatUint(multiplexed.Add(1), 10))
}

var multiplexed atomic.Uint64

func output(ctx context.Context, name string, args ...string) (string, error) {
	stdout, stderr, code, err := run(ctx, nil, name, args...)
	if err != nil || code != 0 {
		return "", failure{err: err, stderr: stderr}
	}
	return stdout, nil
}

func run(ctx context.Context, stdin io.Reader, name string, args ...string) (string, string, int, error) {
	return collectOutput(tracing.CommandContext(ctx, name, args...), stdin)
}

func collectOutput(cmd *tracing.Cmd, stdin io.Reader) (string, string, int, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}
	code, err := exitStatus(cmd.Run())
	if err != nil {
		return "", strings.TrimSpace(stderr.String()), code, err
	}
	return stdout.String(), strings.TrimSpace(stderr.String()), code, nil
}

func exitStatus(waitErr error) (int, error) {
	var exit *exec.ExitError
	switch {
	case waitErr == nil:
		return 0, nil
	case errors.As(waitErr, &exit):
		return exit.ExitCode(), nil
	default:
		return transportFailure, waitErr
	}
}

type failure struct {
	err    error
	stderr string
}

func (f failure) Error() string {
	if f.stderr != "" {
		return f.stderr
	}
	if f.err != nil {
		return f.err.Error()
	}
	return "the command failed and said nothing"
}

func (f failure) Unwrap() error { return f.err }

func terse(err error) string {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return strings.Join(lines, "\n")
}
