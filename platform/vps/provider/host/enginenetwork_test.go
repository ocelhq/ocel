package host

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/enginetest"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

const networkFlag = "'--network' "

type yourNetwork struct {
	binary []byte
	yours  string
	box    string
	board  string
	here   func(string) string
}

func (y yourNetwork) shell(script string) (string, error) {
	said, err := exec.Command("/bin/sh", "-c", y.here(script)).CombinedOutput()
	return string(said), err
}

func onYourNetwork(t *testing.T) yourNetwork {
	t.Helper()
	engineOrSkip(t)
	arch, err := ssh.Architecture(runtime.GOARCH)
	if err != nil {
		t.Skipf("no switchboard is built for a machine reporting %q", runtime.GOARCH)
	}
	boxNetwork := enginetest.Network(t, "box")
	yours, board := enginetest.Network(t, "yours"), probeName(t)+"-board"
	tunneling := enginetest.Network(t, "tunnel")
	t.Cleanup(func() { taken(t, board) })

	dir := enginetest.BindSource(t)
	routedDir, switching := filepath.Join(dir, "routing"), filepath.Join(dir, "switchboard")
	for _, made := range []string{routedDir, switching, filepath.Join(dir, "control"), filepath.Join(dir, "front"), filepath.Join(dir, "connector")} {
		if err := os.MkdirAll(made, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(routedDir, filepath.Base(boxapi.RoutingTablePath)), routingTable(nil).Content, 0o644); err != nil {
		t.Fatal(err)
	}
	binary := switchboardBinary(arch)
	runnable(t, filepath.Join(switching, boxapi.Switchboard), binary, 0o755)
	here := strings.NewReplacer(
		unprivilegedHere(t), "",
		boxapi.FrontDir, filepath.Join(dir, "front"),
		boxapi.SwitchboardControlDir, filepath.Join(dir, "control"),
		boxapi.SwitchboardDir, switching,
		ConnectorRun, filepath.Join(dir, "connector"),
		boxapi.RoutingDir, routedDir,
		routing.Lock, dir,
		shell.Quote(tunnel.Network+":"+boxapi.TunnelListenPort), shell.Quote("127.0.0.1:"+boxapi.TunnelListenPort),
		shell.Quote(tunnel.Network), shell.Quote(tunneling),
		`"`+tunnel.Network+`"`, `"`+tunneling+`"`,
		networkFlag+shell.Quote(ProxyNetwork), networkFlag+shell.Quote("name="+boxNetwork+",alias="+boxapi.Switchboard),
		networkFlag+shell.Quote(yours), networkFlag+shell.Quote("name="+yours+",alias="+boxapi.Switchboard),
		shell.Quote(ProxyNetwork), shell.Quote(boxNetwork),
		shell.Quote(boxapi.Switchboard), shell.Quote(board),
		`"`+ProxyNetwork+`"`, `"`+boxNetwork+`"`,
	).Replace
	return yourNetwork{binary: binary, yours: yours, box: boxNetwork, board: board, here: here}
}

func (y yourNetwork) switchboardOn(t *testing.T, port int) docker.Service {
	t.Helper()
	board := switchboardBox(y.binary, front.Proxy{Manual: &front.Manual{Port: port, Network: y.yours}})
	if said, err := y.shell(board.Writing(docker.ContainerRising)); err != nil {
		t.Fatalf("the write that starts the switchboard on %s = %v, want it serving: it resolves every network it relays from as it starts\n%s\n%s", y.yours, err, said, logsOf(y.board))
	}
	return board
}

func (y yourNetwork) stated(t *testing.T, board docker.Service) bool {
	t.Helper()
	item := containerItem(board, "")
	item.Name = y.board
	binds := make([]string, 0, len(board.Binds))
	for _, bind := range board.Binds {
		binds = append(binds, y.here(bind))
	}
	item.Content = []byte(y.here(string(board.FactsOver(binds))))
	rendered, err := y.shell(containerProbe(board))
	if err != nil {
		t.Fatalf("probe the switchboard: %v\n%s", err, rendered)
	}
	surveyed, _, err := machine.ReadSurvey(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if surveyed[item.ID()] == item.Digest() {
		return true
	}
	box, _ := exec.Command(dockerEngine, "inspect", "--type", "container", "--format", y.here(docker.ContainerFactTemplate), y.board).Output()
	t.Logf("a real engine reports the switchboard as something other than the item ocel writes it from:\n%s",
		compared(canonical(string(box)), strings.TrimSpace(string(item.Content))))
	return false
}

func TestASwitchboardOnYourProxysNetworkJoinsItAndIsReadAsStatedUntilItLeaves(t *testing.T) {
	y := onYourNetwork(t)
	yours := y.yours
	port := freePort(t)
	missing := switchboardBox(y.binary, front.Proxy{Manual: &front.Manual{Port: port, Network: yours + "-gone"}})
	if said, err := y.shell(missing.Writing(docker.ContainerRising)); err == nil || !strings.Contains(said, "proxy.manual.network") {
		t.Errorf("the write onto a network this box does not have = %v, %q, want it refused naming the option that set it", err, said)
	}

	board := y.switchboardOn(t, port)
	if !y.stated(t, board) {
		t.Fatalf("the switchboard ocel started on %s does not read as the item it writes it from", yours)
	}

	if said, err := exec.Command(dockerEngine, "network", "disconnect", yours, y.board).CombinedOutput(); err != nil {
		t.Fatalf("take the switchboard off %s: %v\n%s", yours, err, said)
	}
	if y.stated(t, board) {
		t.Errorf("the switchboard taken off %s still reads as stated, so no bootstrap would put it back where your proxy reaches it", yours)
	}
}

func TestASwitchboardAPruneTookIsStartedAgainOnYourProxysNetworkAndReadAsStated(t *testing.T) {
	y := onYourNetwork(t)
	board := y.switchboardOn(t, freePort(t))
	if said, err := exec.Command(dockerEngine, "rm", "--force", y.board).CombinedOutput(); err != nil {
		t.Fatalf("remove the switchboard as a prune does: %v\n%s", err, said)
	}
	if said, err := exec.Command(dockerEngine, "network", "rm", y.box).CombinedOutput(); err != nil {
		t.Fatalf("remove %s as a prune does: %v\n%s", y.box, err, said)
	}

	gone := board
	gone.Networks = []docker.Network{{Name: y.yours + "-gone", Option: "proxy.manual.network"}}
	if said, err := y.shell(gone.Restoring(docker.ContainerRising, routing.Locked("-x"))); err == nil || !strings.Contains(said, "proxy.manual.network") {
		t.Errorf("starting the switchboard again onto a network this box does not have = %v, %q, want it refused naming the option that set it", err, said)
	}

	if said, err := y.shell(board.Restoring(docker.ContainerRising, routing.Locked("-x"))); err != nil {
		t.Fatalf("starting the switchboard again = %v, want it serving on %s\n%s\n%s", err, y.yours, said, logsOf(y.board))
	}
	if !y.stated(t, board) {
		t.Errorf("the switchboard started again after a prune does not read as the one bootstrap starts on %s, so your proxy would reach nothing until a bootstrap", y.yours)
	}
}
