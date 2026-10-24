//go:build integration && unix

package enginetest

import (
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
)

func aFreeHostPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

func publishing(t *testing.T, of, port string) string {
	t.Helper()
	name := uniqueName("publishing")
	t.Cleanup(func() { exec.Command(engine, "rm", "--force", name).Run() })
	argv := append(append([]string{"run", "--detach", "--name", name}, labelledAs(of)...),
		"--publish", "127.0.0.1:"+port+":"+port, "--entrypoint", "sleep", images.ObjectStore(), "600")
	if said, err := exec.Command(engine, argv...).CombinedOutput(); err != nil {
		t.Fatalf("run a container publishing host port %s: %v\n%s", port, err, said)
	}
	return name
}

func TestHostPortsTakeBackAPortARunThatDiedStillPublishesAndLeaveALiveRunsAlone(t *testing.T) {
	RequireDocker(t)
	died, living := aFreeHostPort(t), aFreeHostPort(t)
	left := publishing(t, aRunThatEnded(t), died)
	owned := publishing(t, runOf(hostname(), os.Getppid()), living)

	HostPorts(t, died, living)

	if exec.Command(engine, "container", "inspect", left).Run() == nil {
		t.Errorf("host port %s is still published by %s, a container of a run that died: a run killed mid-test keeps its proxy on 80 and 443, and the next run to take the ports cannot publish them", died, left)
	}
	if exec.Command(engine, "container", "inspect", owned).Run() != nil {
		t.Errorf("taking host port %s removed %s, a container of a run whose process still runs: it takes another run's container mid-test", living, owned)
	}
}
