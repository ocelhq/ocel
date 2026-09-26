package host

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/enginetest"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func freePort(t *testing.T) int {
	t.Helper()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	return held.Addr().(*net.TCPAddr).Port
}

type yourNetwork struct {
	binary []byte
	yours  string
	box    string
	here   func(string) string
}

func (y yourNetwork) shell(script string) (string, error) {
	said, err := exec.Command("/bin/sh", "-c", y.here(script)).CombinedOutput()
	return string(said), err
}

func onYourNetwork(t *testing.T) yourNetwork {
	t.Helper()
	engineOrSkip(t)
	arch, err := Architecture(runtime.GOARCH)
	if err != nil {
		t.Skipf("no switchboard is built for a machine reporting %q", runtime.GOARCH)
	}
	boxNetwork := enginetest.Network(t)
	yours := probeName(t)
	if said, err := exec.Command(dockerEngine, append(append([]string{"network", "create"}, enginetest.RunLabelArgs(t)...), yours)...).CombinedOutput(); err != nil {
		t.Fatalf("create %s: %v\n%s", yours, err, said)
	}
	t.Cleanup(func() { _ = exec.Command(dockerEngine, "network", "rm", yours).Run() })
	t.Cleanup(func() { taken(t, SwitchboardContainer) })

	dir := enginetest.BindSource(t)
	routing, switching := filepath.Join(dir, "routing"), filepath.Join(dir, "switchboard")
	for _, made := range []string{routing, switching, filepath.Join(dir, "control"), filepath.Join(dir, "front"), filepath.Join(dir, "connector")} {
		if err := os.MkdirAll(made, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(routing, filepath.Base(live.RoutingTable)), routingTable(nil).Content, 0o644); err != nil {
		t.Fatal(err)
	}
	binary := switchboardBinary(arch)
	runnable(t, filepath.Join(switching, switchboard.Name), binary, 0o755)
	here := strings.NewReplacer(
		switchboard.FrontDir, filepath.Join(dir, "front"),
		switchboard.ControlDir, filepath.Join(dir, "control"),
		SwitchboardDir, switching,
		ConnectorRun, filepath.Join(dir, "connector"),
		live.RoutingDir, routing,
		routingLock, dir,
		quoted(ProxyNetwork), quoted(boxNetwork),
		`"`+ProxyNetwork+`"`, `"`+boxNetwork+`"`,
	).Replace
	return yourNetwork{binary: binary, yours: yours, box: boxNetwork, here: here}
}

func (y yourNetwork) standing(t *testing.T, port int) boxContainer {
	t.Helper()
	board := switchboardStanding(y.binary, Front{Manual: &ManualFront{Port: port, Network: y.yours}})
	if said, err := y.shell(board.writing(containerRising)); err != nil {
		t.Fatalf("the write that stands the switchboard on %s = %v, want it serving: it resolves every network it relays from as it starts\n%s\n%s", y.yours, err, said, logsOf(SwitchboardContainer))
	}
	return board
}

func (y yourNetwork) stated(t *testing.T, board boxContainer) bool {
	t.Helper()
	item := board.item("")
	binds := make([]string, 0, len(board.binds))
	for _, bind := range board.binds {
		binds = append(binds, y.here(bind))
	}
	item.Content = []byte(y.here(string(board.factsOver(binds))))
	rendered, err := y.shell(board.probe())
	if err != nil {
		t.Fatalf("probe the switchboard: %v\n%s", err, rendered)
	}
	surveyed, _, err := readSurvey(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if surveyed[item.ID()] == item.Digest() {
		return true
	}
	box, _ := exec.Command(dockerEngine, "inspect", "--type", "container", "--format", y.here(ContainerFactTemplate), SwitchboardContainer).Output()
	t.Logf("a real engine reports the switchboard as something other than the item ocel writes it from:\n%s",
		compared(canonical(string(box)), strings.TrimSpace(string(item.Content))))
	return false
}

func TestASwitchboardOnYourProxysNetworkStandsOnItAndIsReadAsStatedUntilItLeaves(t *testing.T) {
	y := onYourNetwork(t)
	yours := y.yours
	port := freePort(t)
	missing := switchboardStanding(y.binary, Front{Manual: &ManualFront{Port: port, Network: yours + "-gone"}})
	if said, err := y.shell(missing.writing(containerRising)); err == nil || !strings.Contains(said, "proxy.manual.network") {
		t.Errorf("the write onto a network this box does not have = %v, %q, want it refused naming the option that set it", err, said)
	}

	board := y.standing(t, port)
	if !y.stated(t, board) {
		t.Fatalf("the switchboard ocel stood on %s does not read as the item it writes it from", yours)
	}

	if said, err := exec.Command(dockerEngine, "network", "disconnect", yours, SwitchboardContainer).CombinedOutput(); err != nil {
		t.Fatalf("take the switchboard off %s: %v\n%s", yours, err, said)
	}
	if y.stated(t, board) {
		t.Errorf("the switchboard taken off %s still reads as stated, so no bootstrap would put it back where your proxy reaches it", yours)
	}
}

func TestASwitchboardAPruneTookIsStoodAgainOnYourProxysNetworkAndReadAsStated(t *testing.T) {
	y := onYourNetwork(t)
	board := y.standing(t, freePort(t))
	if said, err := exec.Command(dockerEngine, "rm", "--force", SwitchboardContainer).CombinedOutput(); err != nil {
		t.Fatalf("remove the switchboard as a prune does: %v\n%s", err, said)
	}
	if said, err := exec.Command(dockerEngine, "network", "rm", y.box).CombinedOutput(); err != nil {
		t.Fatalf("remove %s as a prune does: %v\n%s", y.box, err, said)
	}

	gone := board
	gone.networks = []userNetwork{{name: y.yours + "-gone", option: "proxy.manual.network"}}
	if said, err := y.shell(gone.restoring(containerRising)); err == nil || !strings.Contains(said, "proxy.manual.network") {
		t.Errorf("standing the switchboard again onto a network this box does not have = %v, %q, want it refused naming the option that set it", err, said)
	}

	if said, err := y.shell(board.restoring(containerRising)); err != nil {
		t.Fatalf("standing the switchboard again = %v, want it serving on %s\n%s\n%s", err, y.yours, said, logsOf(SwitchboardContainer))
	}
	if !y.stated(t, board) {
		t.Errorf("the switchboard stood again after a prune does not read as the one bootstrap stands on %s, so your proxy would reach nothing until a bootstrap", y.yours)
	}
}
