package ssh

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const socketDenied = "permission denied while trying to connect to the docker API at unix:///var/run/docker.sock"

type loginLeftWithoutItsDocker struct{ asked []string }

func (c *loginLeftWithoutItsDocker) Stream(_ context.Context, command string, _ io.Reader) (Result, error) {
	c.asked = append(c.asked, command)
	switch {
	case strings.Contains(command, boxapi.DockerShims+"/docker"):
		return Result{Code: 1}, nil
	case strings.HasPrefix(command, "id -u "):
		return Result{}, nil
	case strings.Contains(command, DockerReach) && strings.HasPrefix(command, "sudo -n -u "+boxapi.DeployLogin):
		return Result{Code: 1, Stderr: socketDenied}, nil
	case strings.Contains(command, DockerReach) && strings.HasPrefix(command, string(viaSudo)):
		return Result{}, nil
	case strings.Contains(command, DockerReach):
		return Result{Code: 1, Stderr: socketDenied}, nil
	}
	return Result{}, nil
}

func (c *loginLeftWithoutItsDocker) Run(ctx context.Context, command string) (string, error) {
	if command == "id -u" {
		return "1000\n", nil
	}
	said, err := c.Stream(ctx, command, nil)
	return said.Stdout, err
}

func (c *loginLeftWithoutItsDocker) RunLines(context.Context, string, func(Line) error) error {
	return nil
}

func (c *loginLeftWithoutItsDocker) Preflight(context.Context) (Facts, error) {
	return Facts{Sudo: true, Systemd: true}, nil
}

func (c *loginLeftWithoutItsDocker) Destination() Destination {
	return Destination{Written: "box.example", Address: "203.0.113.7", Port: 22, User: "ubuntu"}
}

func (c *loginLeftWithoutItsDocker) ForwardPort(context.Context, string) (string, func(), error) {
	return "", nil, errors.New("this box forwards no port")
}

func (c *loginLeftWithoutItsDocker) ForwardSpans() string { return "" }

func TestADeployLoginWhoseCheckedDockerIsNotYetInstalledLeavesDockerToTheLoginThatElevates(t *testing.T) {
	t.Parallel()

	conn := &loginLeftWithoutItsDocker{}
	box := NewBox(func(context.Context) (Conn, error) { return conn, nil }, nil)

	if _, err := box.Run(context.Background(), Docker, "read what the engine runs", "docker ps", nil); err != nil {
		t.Fatalf("docker over a box whose %s exists and whose checked docker does not = %v, want it run by the login that can elevate, as on a box with no %s at all: a bootstrap that stopped between the two is finished by running it again, and its first docker command must not need what it has yet to write",
			boxapi.DeployLogin, err, boxapi.DeployLogin)
	}
	ran := conn.asked[len(conn.asked)-1]
	if !strings.HasPrefix(ran, string(viaSudo)+"sh -c ") {
		t.Errorf("docker ps ran as %q, want it elevated", ran)
	}
}
