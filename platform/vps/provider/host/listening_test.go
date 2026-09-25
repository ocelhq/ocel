package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func unprivileged() *bench {
	b := machine(nil)
	b.facts = session.Facts{Root: false, Systemd: true, Arch: "x86_64"}
	b.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, listeners.TCPPath) {
			return session.Result{Stdout: "  sl  local_address\n"}, true
		}
		return session.Result{}, false
	}
	return b
}

func listenerRead(b *bench) string {
	for _, command := range b.commands() {
		if strings.Contains(command, listeners.TCPPath) {
			return command
		}
	}
	return ""
}

func TestWhatListensOnThisHostIsReadWithoutElevation(t *testing.T) {
	t.Parallel()

	b := unprivileged()
	if _, err := b.host().Listening(context.Background()); err != nil {
		t.Fatalf("Listening() = %v", err)
	}
	read := listenerRead(b)
	if read == "" {
		t.Fatalf("nothing this host ran names %s, so there is no command to hold to the rule:\n%s",
			listeners.TCPPath, strings.Join(b.commands(), "\n"))
	}
	if strings.Contains(read, "sudo") {
		t.Errorf("this host reads what listens on it with %q, and %s is world-readable: a login whose sudoers permits only the seal helper is denied a read it never needed elevation for",
			read, listeners.TCPPath)
	}
}

func TestWhatListensOnThisHostIsNeitherSilencedNorForcedToSucceed(t *testing.T) {
	t.Parallel()

	b := unprivileged()
	if _, err := b.host().Listening(context.Background()); err != nil {
		t.Fatalf("Listening() = %v", err)
	}
	read := listenerRead(b)
	if read == "" {
		t.Fatalf("nothing this host ran names %s, so there is no command to hold to the rule:\n%s",
			listeners.TCPPath, strings.Join(b.commands(), "\n"))
	}
	for _, swallowed := range []string{"2>/dev/null", "|| true"} {
		if strings.Contains(read, swallowed) {
			t.Errorf("this host reads what listens on it with %q, and %q turns a denied read into an empty answer that reads as a port nothing holds",
				read, swallowed)
		}
	}
}

func TestAListenerReadThisHostDeniedIsRefusedWithWhatItSaid(t *testing.T) {
	t.Parallel()

	b := unprivileged()
	b.answer = func(command string) (session.Result, bool) {
		if !strings.Contains(command, listeners.TCPPath) {
			return session.Result{}, false
		}
		denied := session.Result{Code: 1, Stderr: "cat: " + listeners.TCPPath + ": Permission denied"}
		if strings.Contains(command, "|| true") {
			denied.Code, denied.Stderr = 0, ""
		}
		return denied, true
	}
	_, err := b.host().Listening(context.Background())
	if err == nil {
		t.Fatal("Listening() read a denied /proc as a host with nothing bound, and that is reported upstream as a port nothing holds")
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("Listening() = %q, want what the host said about the read it refused", err)
	}
}
