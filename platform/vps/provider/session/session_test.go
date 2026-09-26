package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func TestAMasterThatHasAlreadyIdledOutIsClosedWithoutComplaint(t *testing.T) {
	t.Parallel()

	session := &Session{
		dest:    Destination{Written: "203.0.113.10", User: "ubuntu", Port: 22},
		control: filepath.Join(t.TempDir(), "never-made"),
	}
	if err := session.Close(); err != nil {
		t.Errorf("Close() = %v over a control socket ControlPersist already reaped, want the master gone to read as closed", err)
	}
}

func TestTheControlPathIsShortEnoughForAUnixSocket(t *testing.T) {
	t.Parallel()

	path := multiplex()
	if path == "" {
		t.Skip("this platform multiplexes nothing")
	}
	if len(path) > 100 {
		t.Errorf("the control path is %d bytes (%s), and a unix socket path over 104 leaves ssh unable to bind the master at all", len(path), path)
	}
}

func sshSaying(t *testing.T, resolving, scanning, running string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in ssh is a posix shell script")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"ssh":         "if [ \"$1\" = -G ]; then\n" + resolving + "\nfi\n" + running,
		"ssh-keyscan": scanning,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const (
	resolved = "printf 'hostname 203.0.113.10\\nuser ubuntu\\nport 22\\n'; exit 0"
	keyed    = "echo '203.0.113.10 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl'"
)

func refusedCode(t *testing.T, err error) refusal.Code {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("%v is no refusal the CLI can render", err)
	}
	return refused.Code
}

func TestAHostThatCannotBeReachedIsNotReadyRatherThanDenied(t *testing.T) {
	for name, tc := range map[string]struct {
		resolving, scanning string
		want                refusal.Code
	}{
		"a destination ssh cannot make sense of": {"echo 'command-line line 0: Bad configuration option: foo' >&2; exit 255", keyed, refusal.CodeInvalid},
		"a host no name resolves to":             {resolved, "echo 'getaddrinfo box.invalid: Name or service not known' >&2; exit 1", refusal.CodeNotReady},
		"a port that never answers":              {resolved, "echo 'connect to 203.0.113.10 port 22: Connection timed out' >&2; exit 1", refusal.CodeNotReady},
		"a port that answers with no host key":   {resolved, "exit 0", refusal.CodeNotReady},
	} {
		t.Run(name, func(t *testing.T) {
			sshSaying(t, tc.resolving, tc.scanning, "exit 0")
			_, err := Open(context.Background(), Target{Host: "box.invalid", User: "ubuntu"})
			if got := refusedCode(t, err); got != tc.want {
				t.Errorf("Open() over %s is refused %q, want %q: a host that never answered refused no credential", name, got, tc.want)
			}
		})
	}
}

func TestSSHIsDeniedOnlyWhenTheHostRefusedTheLogin(t *testing.T) {
	box := &Session{target: Target{Host: "203.0.113.10", User: "ubuntu"}, dest: Destination{User: "ubuntu", Written: "203.0.113.10"}}
	for said, want := range map[string]refusal.Code{
		"ubuntu@203.0.113.10: Permission denied (publickey).":                                     refusal.CodeDenied,
		"Received disconnect from 203.0.113.10 port 22:2: Too many authentication failures":       refusal.CodeDenied,
		"ssh: connect to host 203.0.113.10 port 22: Connection refused":                           refusal.CodeNotReady,
		"ssh: connect to host 203.0.113.10 port 22: Connection timed out":                         refusal.CodeNotReady,
		"Connection to 203.0.113.10 closed by remote host.":                                       refusal.CodeNotReady,
		"kex_exchange_identification: read: Connection reset by peer\r\nConnection reset by peer": refusal.CodeNotReady,
	} {
		sshSaying(t, resolved, keyed, "printf '%s\\n' "+quotedForTest(said)+" >&2; exit 255")
		_, err := box.Stream(context.Background(), "true", nil)
		if got := refusedCode(t, err); got != want {
			t.Errorf("ssh that said %q is refused %q, want %q", said, got, want)
		}
	}
}

func TestACommandTheHostRanAndThatFailedIsNeverDenied(t *testing.T) {
	box := &Session{target: Target{Host: "203.0.113.10", User: "ubuntu"}, dest: Destination{User: "ubuntu", Written: "203.0.113.10"}}
	for _, said := range []string{
		"cat: /root/.ssh/authorized_keys: Permission denied",
		"docker: Error response from daemon: failed to create task for container: operation not permitted",
		"write error: No space left on device",
		"sh: 1: Syntax error: end of file unexpected",
	} {
		sshSaying(t, resolved, keyed, "printf '%s\\n' "+quotedForTest(said)+" >&2; exit 1")
		_, err := box.Run(context.Background(), "id -u")
		if got := refusedCode(t, err); got != refusal.CodeNotReady {
			t.Errorf("a command that ran and said %q is refused %q, want %q: the host accepted the login, and what the command hit is no credential problem", said, got, refusal.CodeNotReady)
		}
	}
}

func TestAnSSHFailureShowsTheLineItEndedOnPastTheWarningsBeforeIt(t *testing.T) {
	box := &Session{target: Target{Host: "203.0.113.10", User: "ubuntu"}, dest: Destination{User: "ubuntu", Written: "203.0.113.10"}}
	said := strings.Join([]string{
		"Warning: Permanently added '203.0.113.10' (ED25519) to the list of known hosts.",
		"** WARNING: connection is not using a post-quantum key exchange algorithm.",
		"** This session may be vulnerable to \"store now, decrypt later\" attacks.",
		"** The server may need to be upgraded. See https://openssh.com/pq.html",
		"ubuntu@203.0.113.10: Permission denied (publickey).",
	}, "\n")
	sshSaying(t, resolved, keyed, "printf '%s\\n' "+quotedForTest(said)+" >&2; exit 255")
	_, err := box.Stream(context.Background(), "true", nil)
	if err == nil || !strings.Contains(err.Error(), "Permission denied (publickey)") {
		t.Errorf("the refusal reads %v, and never says how ssh ended: OpenSSH warns for four lines before it says why it stopped", err)
	}
}

func quotedForTest(said string) string {
	return "'" + strings.ReplaceAll(said, "'", `'\''`) + "'"
}
