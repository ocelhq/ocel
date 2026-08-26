package session

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const (
	reach   = 10 * time.Second
	persist = "60s"
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
			"ssh cannot make sense of %q: %s", target.Destination(), problem(err))
	}
	keys, err := offered(ctx, dest)
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeDenied,
			"%s port %d did not answer within %s: %s", dest.Address, dest.Port, reach, problem(err))
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

func (s *Session) Fingerprint() string { return s.anchor.Fingerprint }

func (s *Session) HostKey() providerkit.HostKey { return s.anchor }

func (s *Session) Destination() Destination { return s.dest }

func (s *Session) Run(ctx context.Context, command string) (string, error) {
	rendered, err := output(ctx, "ssh", append(s.args(), s.dest.Written, command)...)
	if err == nil {
		return rendered, nil
	}
	if untrusted(err) {
		if _, trust := classify(s.dest, reoffered(ctx, s.dest), recorded(ctx, s.dest)); trust != nil {
			return "", providerkit.RefuseHostTrust(*trust)
		}
	}
	return "", providerkit.Refuse(providerkit.CodeDenied,
		"%s over ssh: %s", s.dest.Principal(), problem(err))
}

func (s *Session) Close() error {
	if s.control == "" {
		return nil
	}
	return exec.Command("ssh", append(s.args(), "-O", "exit", s.dest.Written)...).Run()
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
			"-o", "ControlPath="+filepath.Join(s.control, "%C"),
			"-o", "ControlPersist="+persist,
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
	return dir
}

func reoffered(ctx context.Context, dest Destination) []providerkit.HostKey {
	keys, err := offered(ctx, dest)
	if err != nil {
		return nil
	}
	return keys
}

func untrusted(err error) bool {
	return strings.Contains(err.Error(), "Host key verification failed")
}

func output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.Stdin = &stdout, &stderr, nil
	if err := cmd.Run(); err != nil {
		return "", failure{err: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return stdout.String(), nil
}

type failure struct {
	err    error
	stderr string
}

func (f failure) Error() string {
	if f.stderr == "" {
		return f.err.Error()
	}
	return f.stderr
}

func (f failure) Unwrap() error { return f.err }

func problem(err error) string {
	if err == nil {
		return ""
	}
	return firstLines(err.Error())
}

func firstLines(rendered string) string {
	lines := strings.Split(strings.TrimSpace(rendered), "\n")
	if len(lines) > 4 {
		lines = lines[:4]
	}
	return strings.Join(lines, "\n")
}
