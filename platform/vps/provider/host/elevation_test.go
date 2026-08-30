package host

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type sudoless struct{ asked []string }

func (c *sudoless) Preflight(context.Context) (session.Facts, error) {
	return session.Facts{Systemd: true}, providerkit.Refuse(providerkit.CodeDenied,
		"ocel-deploy@box.example can neither act as root nor run sudo without a password, and bootstrap writes as root throughout")
}

func (c *sudoless) Run(ctx context.Context, command string) (string, error) {
	said, err := c.Stream(ctx, command, nil)
	return said.Stdout, err
}

func (c *sudoless) Stream(_ context.Context, command string, _ io.Reader) (session.Result, error) {
	c.asked = append(c.asked, command)
	switch {
	case strings.HasPrefix(command, "cat ~/.ssh/authorized_keys"):
		return session.Result{Stdout: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB deploy@ocel\n"}, nil
	case command == "uname -m":
		return session.Result{Stdout: "x86_64\n"}, nil
	default:
		return session.Result{}, nil
	}
}

func (c *sudoless) Destination() session.Destination {
	return session.Destination{Written: "box.example", Address: "203.0.113.7", Port: 22, User: "ocel-deploy"}
}

func (c *sudoless) elevated() []string {
	var over []string
	for _, command := range c.asked {
		if strings.Contains(command, "sudo -n ") {
			over = append(over, command)
		}
	}
	return over
}

func hostFor(conn *sudoless) *Host {
	return New(func(context.Context) (Conn, error) { return conn, nil }, Keys{}, nil)
}

func TestDescribingAHostNeedsNoPowerToWriteToIt(t *testing.T) {
	conn := &sudoless{}
	ctx := context.Background()

	described, err := Bootstrap(hostFor(conn)).Describe(ctx, providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("Describe() = %v, want what this login can see of the host: the preflight reports bootstrap standing through Describe, and a Describe that demands root turns every deploy under %s into a refusal that carries no claims, no standing and no known slugs",
			err, "ocel-deploy")
	}
	if described.Present {
		t.Errorf("Describe() reports the class present against a host that answered no survey line, want it absent")
	}
	if over := conn.elevated(); len(over) != 0 {
		t.Errorf("Describe() ran %q as root, want a description read as the login that asked for it", over)
	}
}

func TestReadingTheHostABootstrapWritesToStillNeedsRoot(t *testing.T) {
	conn := &sudoless{}
	ctx := context.Background()

	_, err := hostFor(conn).Read(ctx, providerkit.ClassProduction)
	if err == nil || !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("Read() = %v, want the refusal naming the grant this login lacks: an apply plans against what root can see, and a plan drawn from a narrower view writes over what it could not read",
			err)
	}
}
