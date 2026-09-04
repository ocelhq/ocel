package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	reach      = 10 * time.Second
	masterIdle = "60s"
)

type Session struct {
	target  Target
	dest    Destination
	anchor  providerkit.HostKey
	control string
}

func Open(ctx context.Context, target Target) (*Session, error) {
	dest, err := resolve(ctx, target)
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"ssh cannot make sense of %q: %s", target.Destination(), terse(err))
	}
	keys, err := offered(ctx, dest)
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"%s port %d did not answer within %s: %s", dest.Address, dest.Port, reach, terse(err))
	}
	if len(keys) == 0 {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"%s port %d offered no ssh host key, so there is nothing to trust", dest.Address, dest.Port)
	}
	anchor, trust := classify(dest, keys, recorded(ctx, dest))
	if trust != nil {
		return nil, providerkit.RefuseHostTrust(*trust)
	}

	session := &Session{target: target, dest: dest, anchor: anchor, control: multiplex()}
	if _, err := session.Run(ctx, "true"); err != nil {
		session.Close()
		return nil, err
	}
	return session, nil
}

func (s *Session) HostKey() providerkit.HostKey { return s.anchor }

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
		return "", providerkit.Refuse(providerkit.CodeDenied,
			"%s over ssh: %s", s.dest.Principal(), terse(result.problem(command)))
	}
	return result.Stdout, nil
}

func (s *Session) Stream(ctx context.Context, command string, stdin io.Reader) (Result, error) {
	stdout, stderr, code, err := run(ctx, stdin, "ssh", append(s.args(), s.dest.Written, command)...)
	if err == nil && code != transportFailure {
		return Result{Stdout: stdout, Stderr: stderr, Code: code}, nil
	}
	if strings.Contains(stderr, "Host key verification failed") {
		keys, _ := offered(ctx, s.dest)
		if _, trust := classify(s.dest, keys, recorded(ctx, s.dest)); trust != nil {
			return Result{}, providerkit.RefuseHostTrust(*trust)
		}
	}
	return Result{}, providerkit.Refuse(providerkit.CodeDenied,
		"%s over ssh: %s", s.dest.Principal(), terse(failure{err: err, stderr: stderr}))
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
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if stdin != nil {
		cmd.Stdin = stdin
	}
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), strings.TrimSpace(stderr.String()), 0, nil
	case errors.As(err, &exit):
		return stdout.String(), strings.TrimSpace(stderr.String()), exit.ExitCode(), nil
	default:
		return "", strings.TrimSpace(stderr.String()), transportFailure, err
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
		lines = lines[:4]
	}
	return strings.Join(lines, "\n")
}
