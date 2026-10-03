package session

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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	reach         = 10 * time.Second
	aliveInterval = 15 * time.Second
	aliveProbes   = 4
	masterIdle    = "60s"
)

type Session struct {
	target  Target
	dest    Destination
	anchor  HostKey
	control string
}

func Open(ctx context.Context, target Target) (*Session, error) {
	dest, err := resolve(ctx, target)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"ssh cannot make sense of %q: %s", target.Destination(), terse(err))
	}
	keys, err := offered(ctx, dest)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"%s port %d did not answer within %s: %s", dest.Address, dest.Port, reach, terse(err))
	}
	if len(keys) == 0 {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"%s port %d offered no ssh host key", dest.Address, dest.Port)
	}
	anchor, trust := classify(dest, keys, recorded(ctx, dest))
	if trust != nil {
		return nil, RefuseHostTrust(*trust)
	}

	session := &Session{target: target, dest: dest, anchor: anchor, control: multiplex()}
	if _, err := session.Run(ctx, "true"); err != nil {
		session.Close()
		return nil, err
	}
	return session, nil
}

func (s *Session) HostKey() HostKey { return s.anchor }

func (s *Session) Destination() Destination { return s.dest }

type Result struct {
	Stdout string
	Stderr string
	Code   int
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
	commandCtx, stop := context.WithCancel(ctx)
	defer stop()

	cmd := s.newSSHCommand(commandCtx, command)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return s.refuseUnreached(ctx, err, "")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return s.refuseUnreached(ctx, err, "")
	}
	cmd.Cancel = func() error {
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
		stderrTail  []byte
	)
	deliver := func(line Line) bool {
		mu.Lock()
		defer mu.Unlock()
		if callbackErr != nil || readErr != nil {
			return false
		}
		if line.Pipe == Stderr {
			stderrTail = keepTail(append(stderrTail, line.Text+"\n"...), keptStderrBytes)
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
	kept := strings.TrimSpace(string(stderrTail))
	code, err := exitStatus(waitErr)
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

func keepTail(kept []byte, limitBytes int) []byte {
	if len(kept) <= limitBytes {
		return kept
	}
	kept = kept[len(kept)-limitBytes:]
	if newline := bytes.IndexByte(kept, '\n'); newline >= 0 && newline < len(kept)-1 {
		kept = kept[newline+1:]
	}
	return kept
}

func (s *Session) Stream(ctx context.Context, command string, stdin io.Reader) (Result, error) {
	stdout, stderr, code, err := collectOutput(s.newSSHCommand(ctx, command), stdin)
	if err == nil && code != transportFailure {
		return Result{Stdout: stdout, Stderr: stderr, Code: code}, nil
	}
	return Result{}, s.refuseUnreached(ctx, err, stderr)
}

func (s *Session) newSSHCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "ssh", append(s.args(), s.dest.Written, command)...)
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

func (s *Session) Close() error {
	if s.control == "" {
		return nil
	}
	_, err := output(context.Background(), "ssh", append(s.args(), "-O", "exit", s.dest.Written)...)
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
		"-o", "ConnectTimeout="+strconv.Itoa(int(reach.Seconds())),
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
	return collectOutput(exec.CommandContext(ctx, name, args...), stdin)
}

func collectOutput(cmd *exec.Cmd, stdin io.Reader) (string, string, int, error) {
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
