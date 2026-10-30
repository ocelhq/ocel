package host

import (
	"bytes"
	"context"
	"debug/elf"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestTheAgentListensOnASocketSystemdBindsBeforeTheEngineAndKeepsAcrossARestart(t *testing.T) {
	t.Parallel()

	socket := string(itemAt(t, LiveItems(testAssets, ssh.AMD64), machine.KindFile, liveSocketFile).Content)
	for _, want := range []string{
		"ListenStream=" + boxapi.SocketPath,
		"Before=" + dockerUnit,
		"SocketMode=0666",
		"WantedBy=sockets.target",
	} {
		if !strings.Contains(socket, want) {
			t.Errorf("the socket unit reads:\n%s\nand never says %q", socket, want)
		}
	}
	service := string(itemAt(t, LiveItems(testAssets, ssh.AMD64), machine.KindFile, liveUnitFile).Content)
	for _, want := range []string{
		"ExecStart=" + LiveBinary,
		"Requires=" + LiveSocketUnit,
		"ProtectSystem=strict",
		"NoNewPrivileges=yes",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(service, want) {
			t.Errorf("the service unit reads:\n%s\nand never says %q", service, want)
		}
	}
	if strings.Contains(service, "User=") {
		t.Errorf("the service unit reads:\n%s\nand names a user: the agent reads the tier key, which is root's alone", service)
	}
	if !strings.HasPrefix(boxapi.SocketPath, boxapi.SocketDir+"/") || boxapi.SocketDir == ConnectorRun || strings.HasPrefix(boxapi.SocketDir, ConnectorRun+"/") {
		t.Errorf("the socket is at %s, under %s, which %s owns and could rename out from under every container", boxapi.SocketPath, boxapi.SocketDir, deployUser)
	}
}

func TestTheUnitsAreWrittenAfterWhatTheyWatchAndTheServiceAfterItsSocket(t *testing.T) {
	t.Parallel()

	items := Items(testAssets, environment.TierProduction, nil, ssh.AMD64, front.Proxy{})
	at := func(kind, name string) int {
		return slices.IndexFunc(items, func(item machine.Item) bool { return item.Kind == kind && item.Name == name })
	}
	socket, service := at(machine.KindUnit, LiveSocketUnit), at(machine.KindUnit, LiveService)
	if socket < 0 || service < 0 || socket > service {
		t.Fatalf("the socket unit is at %d and the service at %d: a restarted service takes the socket systemd already owns", socket, service)
	}
	for _, unit := range []int{socket, service} {
		for _, watched := range items[unit].Watch {
			if written := at(machine.KindFile, watched); written < 0 || written > unit {
				t.Errorf("%s watches %s, which is written at %d, after the unit at %d", items[unit].Name, watched, written, unit)
			}
		}
	}
	if items[service].Content == nil || !bytes.Equal(items[service].Content, machine.UnitWatchFacts(liveServiceUnit(), liveAgent(testAssets, ssh.AMD64))) {
		t.Error("the service's facts do not follow the unit file and the agent, so a changed agent would not restart it")
	}
	for _, item := range LiveItems(testAssets, ssh.AMD64) {
		if item.Owner != machine.RootOwner {
			t.Errorf("%s is written to %s, and the agent is root's alone", item.ID(), item.Owner)
		}
	}
	if agent := items[at(machine.KindFile, LiveBinary)]; agent.Owner != machine.RootOwner {
		t.Errorf("%s is written to %s, and the agent is root's alone", agent.ID(), agent.Owner)
	}
}

func TestTheAgentAndTheRuntimeAreStaticBinariesForEachArchitectureTheBoxRuns(t *testing.T) {
	t.Parallel()

	for arch, machine := range map[string]elf.Machine{ssh.AMD64: elf.EM_X86_64, ssh.ARM64: elf.EM_AARCH64} {
		runtime, err := ContainerRuntime(testAssets, arch)
		if err != nil {
			t.Fatalf("ContainerRuntime(testAssets, %s) = %v", arch, err)
		}
		for what, body := range map[string][]byte{"agent": liveAgent(testAssets, arch), "runtime": runtime} {
			binary, err := elf.NewFile(bytes.NewReader(body))
			if err != nil {
				t.Fatalf("the %s for %s is no ELF binary: %v", what, arch, err)
			}
			if binary.Machine != machine {
				t.Errorf("the %s for %s is built for %s", what, arch, binary.Machine)
			}
			if section := binary.Section(".interp"); section != nil {
				t.Errorf("the %s for %s asks for a dynamic loader, and it runs in images that may ship none", what, arch)
			}
		}
	}
	if _, err := ContainerRuntime(testAssets, "riscv64"); err == nil {
		t.Error("ContainerRuntime(testAssets, riscv64) handed something back, and ocel builds the runtime for amd64 and arm64 alone")
	}
	reported, _ := ContainerRuntime(testAssets, "x86_64")
	named, _ := ContainerRuntime(testAssets, ssh.AMD64)
	if !bytes.Equal(reported, named) {
		t.Error("ContainerRuntime(testAssets, x86_64) is not the amd64 build, and an image reports its architecture either way")
	}
}

func TestTheLastDestroyTakesTheAgentAndItsUnitsAndASiblingTierKeepsThem(t *testing.T) {
	t.Parallel()

	production, preview := environment.TierProduction, environment.TierPreview
	keys := []byte(aKey + "\n")
	current := Reading{Assets: testAssets, Arch: ssh.AMD64, Tier: production, Keys: keys, Observed: machine.DigestsOf(Items(testAssets, production, keys, ssh.AMD64, front.Proxy{}))}
	beside := Reading{Assets: testAssets, Arch: ssh.AMD64, Tier: preview, Keys: keys, Observed: machine.DigestsOf(Items(testAssets, preview, keys, ssh.AMD64, front.Proxy{}))}
	for _, name := range []string{LiveService, LiveSocketUnit, LiveBinary, liveUnitFile, liveSocketFile} {
		if kept := removalOf(removing(current, beside, appsPresent{}), name); kept.Action == provider.ActionDelete {
			t.Errorf("destroying one tier takes %s, and the sibling tier's containers still read their values through it", name)
		}
		if gone := removalOf(removing(current, Reading{Assets: testAssets, Arch: ssh.AMD64, Tier: preview, Observed: map[string]string{}}, appsPresent{}), name); gone.Action != provider.ActionDelete {
			t.Errorf("destroying the last tier plans %s as %q", name, gone.Action)
		}
	}

	box := newBench(map[environment.Tier][]machine.Item{production: bootstrapped(t, production)})
	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), production, progress.Discard()); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := strings.Join(box.commands(), "\n")
	for _, unit := range []string{LiveService, LiveSocketUnit} {
		if !strings.Contains(taken, "systemctl disable --now "+shell.Quote(unit)) {
			t.Errorf("the last destroy never stopped %s:\n%s", unit, taken)
		}
	}
	if !strings.Contains(taken, "rm -rf "+shell.Quote(LiveBinary)) {
		t.Errorf("the last destroy left %s:\n%s", LiveBinary, taken)
	}
}

func TestTheDeployLoginIsToldItHasNoHandInTheAgent(t *testing.T) {
	t.Parallel()

	var named bool
	for _, grant := range Grants(environment.TierProduction, front.Proxy{}) {
		if !strings.Contains(grant.Name, boxapi.SocketPath) {
			continue
		}
		named = true
		for _, want := range []string{LiveBinary, LiveService, deployUser, "read-only", "tier key"} {
			if !strings.Contains(grant.Detail, want) {
				t.Errorf("the grant reads %q and never says %q", grant.Detail, want)
			}
		}
	}
	if !named {
		t.Error("`ocel permissions deploy` never mentions the agent every container reads its values through")
	}
}
