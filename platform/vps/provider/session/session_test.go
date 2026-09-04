package session

import (
	"path/filepath"
	"testing"
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
