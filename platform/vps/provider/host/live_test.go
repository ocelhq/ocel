package host

import (
	"bytes"
	"context"
	"debug/elf"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func TestTheAgentListensOnASocketSystemdBindsBeforeTheEngineAndKeepsAcrossARestart(t *testing.T) {
	t.Parallel()

	socket := string(itemAt(t, LiveItems(ArchAMD64), KindFile, liveSocketFile).Content)
	for _, want := range []string{
		"ListenStream=" + live.SocketPath,
		"Before=" + dockerUnit,
		"SocketMode=0666",
		"WantedBy=sockets.target",
	} {
		if !strings.Contains(socket, want) {
			t.Errorf("the socket unit reads:\n%s\nand never says %q", socket, want)
		}
	}
	service := string(itemAt(t, LiveItems(ArchAMD64), KindFile, liveUnitFile).Content)
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
		t.Errorf("the service unit reads:\n%s\nand names a user: the agent reads the class key, which is root's alone", service)
	}
	if !strings.HasPrefix(live.SocketPath, live.SocketDir+"/") || live.SocketDir == ConnectorRun || strings.HasPrefix(live.SocketDir, ConnectorRun+"/") {
		t.Errorf("the socket stands at %s, under %s, which %s owns and could rename out from under every container", live.SocketPath, live.SocketDir, deployUser)
	}
}

func TestTheUnitsAreWrittenAfterWhatTheyWatchAndTheServiceAfterItsSocket(t *testing.T) {
	t.Parallel()

	items := LiveItems(ArchAMD64)
	at := func(kind, name string) int {
		return slices.IndexFunc(items, func(item Item) bool { return item.Kind == kind && item.Name == name })
	}
	socket, service := at(KindUnit, LiveSocketUnit), at(KindUnit, LiveService)
	if socket < 0 || service < 0 || socket > service {
		t.Fatalf("the socket unit stands at %d and the service at %d: a restarted service takes the socket systemd already holds", socket, service)
	}
	for _, unit := range []int{socket, service} {
		for _, watched := range items[unit].Watch {
			if written := at(KindFile, watched); written < 0 || written > unit {
				t.Errorf("%s watches %s, which is written at %d, after the unit at %d", items[unit].Name, watched, written, unit)
			}
		}
	}
	if items[service].Content == nil || !bytes.Equal(items[service].Content, unitWatchFacts(liveServiceUnit(), liveAgent(ArchAMD64))) {
		t.Error("the service's facts do not follow the unit file and the agent, so a changed agent would not restart it")
	}
	for _, item := range items {
		if item.Owner != rootOwner {
			t.Errorf("%s is written to %s, and the agent is root's alone", item.ID(), item.Owner)
		}
	}
}

func TestTheAgentAndTheRuntimeAreStaticBinariesForEachArchitectureTheBoxRuns(t *testing.T) {
	t.Parallel()

	for arch, machine := range map[string]elf.Machine{ArchAMD64: elf.EM_X86_64, ArchARM64: elf.EM_AARCH64} {
		runtime, err := ContainerRuntime(arch)
		if err != nil {
			t.Fatalf("ContainerRuntime(%s) = %v", arch, err)
		}
		for what, body := range map[string][]byte{"agent": liveAgent(arch), "runtime": runtime} {
			binary, err := elf.NewFile(bytes.NewReader(body))
			if err != nil {
				t.Fatalf("the %s for %s is no ELF binary: %v", what, arch, err)
			}
			if binary.Machine != machine {
				t.Errorf("the %s for %s is built for %s", what, arch, binary.Machine)
			}
			if section := binary.Section(".interp"); section != nil {
				t.Errorf("the %s for %s asks for a dynamic loader, and it runs in images that may carry none", what, arch)
			}
		}
	}
	if _, err := ContainerRuntime("riscv64"); err == nil {
		t.Error("ContainerRuntime(riscv64) handed something back, and ocel builds the runtime for amd64 and arm64 alone")
	}
	reported, _ := ContainerRuntime("x86_64")
	named, _ := ContainerRuntime(ArchAMD64)
	if !bytes.Equal(reported, named) {
		t.Error("ContainerRuntime(x86_64) is not the amd64 build, and an image reports its architecture either way")
	}
}

func TestTheLastDestroyTakesTheAgentAndItsUnitsAndASiblingClassKeepsThem(t *testing.T) {
	t.Parallel()

	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	keys := []byte(aKey + "\n")
	standing := Reading{Arch: ArchAMD64, Class: production, Keys: keys, Observed: digests(Items(production, keys, ArchAMD64, Front{}))}
	beside := Reading{Arch: ArchAMD64, Class: preview, Keys: keys, Observed: digests(Items(preview, keys, ArchAMD64, Front{}))}
	for _, name := range []string{LiveService, LiveSocketUnit, LiveBinary, liveUnitFile, liveSocketFile} {
		if kept := removalOf(removing(standing, beside, appsStanding{}), name); kept.action == providerkit.ActionDelete {
			t.Errorf("destroying one class takes %s, and the sibling class's containers still read their values through it", name)
		}
		if gone := removalOf(removing(standing, Reading{Arch: ArchAMD64, Class: preview, Observed: map[string]string{}}, appsStanding{}), name); gone.action != providerkit.ActionDelete {
			t.Errorf("destroying the last class plans %s as %q", name, gone.action)
		}
	}

	stood := machine(map[providerkit.Class][]Item{production: bootstrapped(t, production)})
	if err := Bootstrap(stood.host(), testVendor, "shop").Remove(context.Background(), production, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := strings.Join(stood.commands(), "\n")
	for _, unit := range []string{LiveService, LiveSocketUnit} {
		if !strings.Contains(taken, "systemctl disable --now "+quoted(unit)) {
			t.Errorf("the last destroy never stopped %s:\n%s", unit, taken)
		}
	}
	if !strings.Contains(taken, "rm -rf "+quoted(LiveBinary)) {
		t.Errorf("the last destroy left %s:\n%s", LiveBinary, taken)
	}
}

func TestTheDeployLoginIsToldItHasNoHandInTheAgent(t *testing.T) {
	t.Parallel()

	var named bool
	for _, grant := range Grants(providerkit.ClassProduction) {
		if !strings.Contains(grant.Name, live.SocketPath) {
			continue
		}
		named = true
		for _, want := range []string{LiveBinary, LiveService, deployUser, "read-only", "class key"} {
			if !strings.Contains(grant.Detail, want) {
				t.Errorf("the grant reads %q and never says %q", grant.Detail, want)
			}
		}
	}
	if !named {
		t.Error("`ocel permissions deploy` never mentions the agent every container reads its values through")
	}
}
