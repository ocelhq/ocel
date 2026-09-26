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

func TestASwitchboardOnYourProxysNetworkStandsOnItAndIsReadAsStatedUntilItLeaves(t *testing.T) {
	engineOrSkip(t)
	arch, err := Architecture(runtime.GOARCH)
	if err != nil {
		t.Skipf("no switchboard is built for a machine reporting %q", runtime.GOARCH)
	}
	boxNetwork := enginetest.Network(t)
	yours := probeName(t)
	if said, err := exec.Command(dockerEngine, append(append([]string{"network", "create"}, enginetest.Labelled(t)...), yours)...).CombinedOutput(); err != nil {
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
	shell := func(script string) (string, error) {
		said, err := exec.Command("/bin/sh", "-c", here(script)).CombinedOutput()
		return string(said), err
	}

	port := freePort(t)
	missing := switchboardStanding(binary, Front{Manual: &ManualFront{Port: port, Network: yours + "-gone"}})
	if said, err := shell(missing.writing(containerRising)); err == nil || !strings.Contains(said, "proxy.manual.network") {
		t.Errorf("the write onto a network this box does not have = %v, %q, want it refused naming the option that set it", err, said)
	}

	board := switchboardStanding(binary, Front{Manual: &ManualFront{Port: port, Network: yours}})
	if said, err := shell(board.writing(containerRising)); err != nil {
		t.Fatalf("the write that stands the switchboard on %s = %v, want it serving: it resolves every network it relays from as it starts\n%s\n%s", yours, err, said, logsOf(SwitchboardContainer))
	}
	stated := board.item("")
	binds := make([]string, 0, len(board.binds))
	for _, bind := range board.binds {
		binds = append(binds, here(bind))
	}
	stated.Content = []byte(here(string(board.factsOver(binds))))
	read := func() string {
		rendered, err := shell(board.probe())
		if err != nil {
			t.Fatalf("probe the switchboard: %v\n%s", err, rendered)
		}
		observed, _, err := readSurvey(rendered)
		if err != nil {
			t.Fatal(err)
		}
		return observed[stated.ID()]
	}
	if read() != stated.Digest() {
		box, _ := exec.Command(dockerEngine, "inspect", "--type", "container", "--format", here(ContainerFactTemplate), SwitchboardContainer).Output()
		t.Fatalf("a real engine reports the switchboard on %s as something other than the item ocel writes it from:\n%s", yours,
			compared(canonical(string(box)), strings.TrimSpace(string(stated.Content))))
	}

	if said, err := exec.Command(dockerEngine, "network", "disconnect", yours, SwitchboardContainer).CombinedOutput(); err != nil {
		t.Fatalf("take the switchboard off %s: %v\n%s", yours, err, said)
	}
	if read() == stated.Digest() {
		t.Errorf("the switchboard taken off %s still reads as stated, so no bootstrap would put it back where your proxy reaches it", yours)
	}
}
