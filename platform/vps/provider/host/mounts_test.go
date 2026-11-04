package host

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type engineReport struct {
	facts   string
	inside  map[string]string
	stats   map[string]string
	started int
}

func probedAs(t *testing.T, k kernel, container docker.Service, engine engineReport) string {
	t.Helper()
	dir := t.TempDir()
	said := filepath.Join(dir, "facts")
	if err := os.WriteFile(said, []byte(engine.facts), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := "exit 1"
	if engine.started != 0 {
		inside = fmt.Sprintf("exit %d", engine.started)
	}
	if engine.inside != nil {
		inside = "shift 2\nfor path; do\ncase \"$path\" in\n"
		for path, device := range engine.inside {
			inside += shell.Quote(path) + ") echo " + shell.Quote(path+" "+device) + " ;;\n"
		}
		inside += "esac\ndone\nexit 0"
	}
	executable(t, filepath.Join(dir, dockerEngine), "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"inspect) case \"$*\" in *State.Pid*) echo 4242 ;; *) cat "+shell.Quote(said)+" ;; esac ;;\n"+
		"exec) "+inside+" ;;\n"+
		"*) exit 1 ;;\n"+
		"esac\n")
	stat := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in\n"
	for path, device := range engine.stats {
		stat += shell.Quote(path) + ") echo " + shell.Quote(device) + " ;;\n"
	}
	executable(t, filepath.Join(dir, "stat"), stat+"*) echo \"stat: cannot statx '$last': Permission denied\" >&2; exit 1 ;;\nesac\n")
	for _, tool := range []string{"sha256sum", "cut", "cat", "sort", "grep"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(found, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sh", "-c", k.script(containerProbe(container)))
	cmd.Env = []string{"PATH=" + dir}
	rendered, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe %s: %v", container.Name, err)
	}
	observed, _, err := machine.ReadSurvey(string(rendered))
	if err != nil {
		t.Fatal(err)
	}
	return observed[containerItem(container, "").ID()]
}

func engineSays(container docker.Service, migrate string) string {
	var said []string
	for line := range strings.Lines(string(container.Facts())) {
		if !strings.HasPrefix(line, docker.MigrateFact) && !strings.HasPrefix(line, docker.MountsFact) {
			said = append(said, strings.TrimSuffix(line, "\n"))
		}
	}
	if container.Migrates {
		said = append(said, docker.MigrateFact+migrate)
	}
	return strings.Join(said, "\n")
}

func mountedAs(container docker.Service, moved int) (inside, stats map[string]string) {
	inside, stats = map[string]string{}, map[string]string{}
	for at, bind := range container.Binds {
		source, dest, _ := strings.Cut(bind, ":")
		dest, _, _ = strings.Cut(dest, ":")
		device := fmt.Sprintf("2049:%d", 100+at)
		inside[dest] = device
		stats[source] = device
		if at == moved {
			stats[source] = fmt.Sprintf("2049:%d", 900+at)
		}
	}
	return inside, stats
}

func TestAContainerWithAMountTheHostNoLongerHasIsDrift(t *testing.T) {
	t.Parallel()

	for _, container := range []docker.Service{frontProxy(), switchboardBox(nil, front.Proxy{})} {
		stated := containerItem(container, "").Digest()
		facts := engineSays(container, docker.MigrateSet)
		current, _ := mountedAs(container, -1)
		inside, moved := mountedAs(container, len(container.Binds)-1)
		_, unmoved := mountedAs(container, -1)
		for what, probed := range map[string]struct {
			engine  engineReport
			current bool
		}{
			"every mount still the host's own":                      {engineReport{facts: facts, inside: current, stats: unmoved}, true},
			"a mount whose source was replaced, read with no /proc": {engineReport{facts: facts, inside: inside, stats: moved}, false},
			"a container the engine will not exec into":             {engineReport{facts: facts, stats: moved}, true},
			"a container that cannot find the program it is asked":  {engineReport{facts: facts, stats: moved, started: 127}, false},
			"a container that cannot start the program it is asked": {engineReport{facts: facts, stats: moved, started: 126}, false},
			"a source the host will not stat":                       {engineReport{facts: facts, inside: inside}, true},
		} {
			observed := probedAs(t, kernelMigrating(t, true), container, probed.engine)
			if (observed == stated) != probed.current {
				t.Errorf("%s with %s reads as current=%v, want %v: a container reading a mount the host replaced serves what is no longer there, and only a new container picks the new one up",
					container.Name, what, observed == stated, probed.current)
			}
		}
	}
}

func TestAContainerWithAMountTheHostNoLongerHasIsPlannedBack(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	keys := []byte(aKey + "\n")
	items := Items(testAssets, tier, keys, ssh.AMD64, front.Proxy{})
	minted := []byte("the key this box minted for itself")
	for _, item := range []machine.Item{frontItem(), boardItem()} {
		moved := bytes.Replace(item.Content, []byte(docker.MountsFact+docker.MountsIntact), []byte(docker.MountsFact+docker.MountsMoved), 1)
		if bytes.Equal(moved, item.Content) {
			t.Fatalf("%s is surveyed without its mounts, so what a moved one proves here is nothing", item.Name)
		}
		observed := machine.DigestsOf(items)
		observed[item.ID()] = machine.DigestOf(machine.KindContainer, item.Name, 0, machine.RootOwner, machine.ContentSum(moved))
		read := Reading{Assets: testAssets,
			Tier: tier, Present: true, Keys: keys, Arch: ssh.AMD64, Observed: observed,
			Seal: machine.Seal{Fingerprint: machine.ContentSum(minted)},
			Stamp: machine.Stamp{
				State: machine.StateComplete,
				Seal:  machine.Seal{Fingerprint: machine.ContentSum(minted)}, Digests: machine.DigestsOf(items),
			},
		}
		if back := planFor(planned(read), item.ID()); back.Action != provider.ActionUpdate {
			t.Errorf("%s reading a mount the host replaced plans %q, want it recreated: the box would call itself current while it serves a directory that is gone", item.Name, back.Action)
		}
		if read.upToDate() {
			t.Errorf("a box whose %s reads a mount the host replaced reports itself up to date", item.Name)
		}
	}
}
