package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
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

func TestACommandOverAConnectionTheHostStoppedAnsweringEndsInsteadOfHanging(t *testing.T) {
	t.Parallel()

	for _, session := range []*Session{
		{dest: Destination{Written: "203.0.113.10"}},
		{dest: Destination{Written: "203.0.113.10"}, control: filepath.Join(t.TempDir(), "master")},
	} {
		args := strings.Join(session.args(), " ")
		for _, option := range []string{"-o ServerAliveInterval=15", "-o ServerAliveCountMax=4"} {
			if !strings.Contains(args, option) {
				t.Errorf("ssh runs with %q, want %s: without it a connection the host stopped answering holds a command open until the kernel gives up on it, hours later", args, option)
			}
		}
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

func closedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func resolvedTo(port int) string {
	return fmt.Sprintf("printf 'hostname 127.0.0.1\\nuser ubuntu\\nport %d\\n'; exit 0", port)
}

func TestAPortThatRefusesTheConnectionIsReportedAsRefusedRatherThanSilent(t *testing.T) {
	port := closedPort(t)
	sshSaying(t, resolvedTo(port), "exit 1", "exit 0")

	_, err := Open(context.Background(), Target{Host: "box.invalid", User: "ubuntu"})
	want := fmt.Sprintf("127.0.0.1 port %d refused the connection: nothing accepts ssh there", port)
	if err == nil || err.Error() != want {
		t.Errorf("Open() over a port that refused the connection = %v, want %q: ssh-keyscan says nothing when refused, and a refusal read as a timeout sends the reader waiting on a box that answered at once", err, want)
	}
}

func listening(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener.Addr().(*net.TCPAddr).Port
}

func TestAProbeThatSpentItsWholeWindowSilentlyIsReportedAsATimeout(t *testing.T) {
	t.Parallel()

	port := listening(t)
	dest := Destination{Address: "127.0.0.1", Port: port}
	err := refuseUnanswered(context.Background(), dest, failure{}, time.Now().Add(-time.Second))
	want := fmt.Sprintf("127.0.0.1 port %d did not answer within 10s", port)
	if err == nil || err.Error() != want {
		t.Errorf("refuseUnanswered() after ssh-keyscan waited out the limit = %v, want %q", err, want)
	}
}

func TestAPortThatAcceptsTheConnectionButOffersNoHostKeyIsReportedAsSuch(t *testing.T) {
	port := listening(t)
	sshSaying(t, resolvedTo(port), "exit 1", "exit 0")

	_, err := Open(context.Background(), Target{Host: "box.invalid", User: "ubuntu"})
	want := fmt.Sprintf("127.0.0.1 port %d accepted the connection but offered no ssh host key", port)
	if err == nil || err.Error() != want {
		t.Errorf("Open() over a port that accepts but never speaks ssh = %v, want %q", err, want)
	}
}

func TestAProbeFailureSSHKeyscanExplainedCarriesItsOwnWords(t *testing.T) {
	sshSaying(t, resolved, "echo 'getaddrinfo box.invalid: Name or service not known' >&2; exit 1", "exit 0")

	_, err := Open(context.Background(), Target{Host: "box.invalid", User: "ubuntu"})
	want := "203.0.113.10 port 22 is unreachable: getaddrinfo box.invalid: Name or service not known"
	if err == nil || err.Error() != want {
		t.Errorf("Open() = %v, want %q: what ssh-keyscan said is the diagnosis", err, want)
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

func TestASessionNeverRunsAsManyCommandsAtOnceAsSSHDAllowsOneConnection(t *testing.T) {
	scratch := t.TempDir()
	running, counts := filepath.Join(scratch, "running"), filepath.Join(scratch, "counts")
	if err := os.Mkdir(running, 0o755); err != nil {
		t.Fatal(err)
	}
	box := newSessionRunning(t, fmt.Sprintf("touch %[1]s/$$; sleep 0.5; ls %[1]s | wc -l >> %[2]s; rm %[1]s/$$",
		quotedForTest(running), quotedForTest(counts)))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var commands sync.WaitGroup
	for i := range 20 {
		commands.Go(func() {
			var err error
			switch i % 3 {
			case 0:
				_, err = box.Run(ctx, "seal-helper open")
			case 1:
				_, err = box.Stream(ctx, "seal-helper open", nil)
			default:
				err = box.RunLines(ctx, "seal-helper open", func(Line) error { return nil })
			}
			if err != nil {
				t.Errorf("command %d = %v", i, err)
			}
		})
	}
	commands.Wait()

	said, err := os.ReadFile(counts)
	if err != nil {
		t.Fatal(err)
	}
	most := 0
	for line := range strings.Lines(string(said)) {
		count, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			t.Fatal(err)
		}
		most = max(most, count)
	}
	if most >= 10 {
		t.Errorf("the session ran %d commands at once over its master, and sshd refuses a connection more than its default MaxSessions of 10", most)
	}
}

func TestACommandWaitingForTheSessionGivesUpWhenItsContextEnds(t *testing.T) {
	box := newSessionRunning(t, "exec sleep 30")

	holding, release := context.WithCancel(context.Background())
	var holders sync.WaitGroup
	t.Cleanup(func() {
		release()
		holders.Wait()
	})
	for range 10 {
		holders.Go(func() { _ = box.RunLines(holding, "logs --follow", func(Line) error { return nil }) })
	}
	time.Sleep(200 * time.Millisecond)

	for name, command := range map[string]func(context.Context) error{
		"Run":      func(ctx context.Context) error { _, err := box.Run(ctx, "true"); return err },
		"Stream":   func(ctx context.Context) error { _, err := box.Stream(ctx, "true", nil); return err },
		"RunLines": func(ctx context.Context) error { return box.RunLines(ctx, "true", func(Line) error { return nil }) },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		returned := make(chan error, 1)
		go func() { returned <- command(ctx) }()
		select {
		case err := <-returned:
			if err == nil {
				t.Errorf("%s() = nil, want an error once its context ended while every slot was held", name)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s() was still waiting for the session two seconds after its context ended", name)
		}
		cancel()
	}
}
