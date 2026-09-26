package session

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
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

func TestAFailedRemoteCommandIsDeniedOnlyWhenTheHostRefusedIt(t *testing.T) {
	t.Parallel()

	box := &Session{dest: Destination{User: "ubuntu", Written: "54.84.109.35"}}
	for said, want := range map[string]providerkit.Code{
		"ubuntu@54.84.109.35: Permission denied (publickey).":                                     providerkit.CodeDenied,
		"sudo: a password is required":                                                            providerkit.CodeDenied,
		"ubuntu is not in the sudoers file.":                                                      providerkit.CodeDenied,
		"+ sh -c apt-get -qq update >/dev/null":                                                   providerkit.CodeNotReady,
		"write error: No space left on device":                                                    providerkit.CodeNotReady,
		"sh: 1: Syntax error: end of file unexpected":                                             providerkit.CodeNotReady,
		"Connection to 54.84.109.35 closed by remote host.":                                       providerkit.CodeNotReady,
		"E: Failed to fetch http://us-east-1.ec2.archive.ubuntu.com/ubuntu/dists/noble/InRelease": providerkit.CodeNotReady,
	} {
		var refused providerkit.Refusal
		if !errors.As(box.refused(said), &refused) {
			t.Fatalf("refused(%q) is no refusal the CLI can render", said)
		}
		if refused.Code != want {
			t.Errorf("a command that said %q is refused %q, want %q: a stalled mirror, a full disk and a syntax error are no permissions problem", said, refused.Code, want)
		}
	}
}

func TestAFailedRemoteCommandShowsTheLinesItEndedOn(t *testing.T) {
	t.Parallel()

	box := &Session{dest: Destination{User: "ubuntu", Written: "54.84.109.35"}}
	said := strings.Join([]string{
		"+ sh -c apt-get -qq update >/dev/null",
		"+ sh -c apt-get -qq install ca-certificates curl >/dev/null",
		"+ sh -c install -m 0755 -d /etc/apt/keyrings",
		"+ sh -c curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc",
		"curl: (28) Connection timed out after 30001 milliseconds",
	}, "\n")
	if message := box.refused(said).Error(); !strings.Contains(message, "curl: (28) Connection timed out") {
		t.Errorf("the refusal reads %q, and never says how the command ended", message)
	}
}
