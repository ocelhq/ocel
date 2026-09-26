package host

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type engineReport struct {
	facts   string
	inside  map[string]string
	stats   map[string]string
	started int
}

func probedAs(t *testing.T, k kernel, container boxContainer, engine engineReport) string {
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
		for path, held := range engine.inside {
			inside += quoted(path) + ") echo " + quoted(path+" "+held) + " ;;\n"
		}
		inside += "esac\ndone\nexit 0"
	}
	executable(t, filepath.Join(dir, dockerEngine), "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"inspect) case \"$*\" in *State.Pid*) echo 4242 ;; *) cat "+quoted(said)+" ;; esac ;;\n"+
		"exec) "+inside+" ;;\n"+
		"*) exit 1 ;;\n"+
		"esac\n")
	stat := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in\n"
	for path, held := range engine.stats {
		stat += quoted(path) + ") echo " + quoted(held) + " ;;\n"
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
	cmd := exec.Command("/bin/sh", "-c", k.script(container.probe()))
	cmd.Env = []string{"PATH=" + dir}
	rendered, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe %s: %v", container.name, err)
	}
	observed, _, err := readSurvey(string(rendered))
	if err != nil {
		t.Fatal(err)
	}
	return observed[container.item("").ID()]
}

func engineSays(container boxContainer, migrate string) string {
	var said []string
	for line := range strings.Lines(string(container.facts())) {
		if !strings.HasPrefix(line, migrateFact) && !strings.HasPrefix(line, mountsFact) {
			said = append(said, strings.TrimSuffix(line, "\n"))
		}
	}
	if container.migrates {
		said = append(said, migrateFact+migrate)
	}
	return strings.Join(said, "\n")
}

func mountedAs(container boxContainer, moved int) (inside, stats map[string]string) {
	inside, stats = map[string]string{}, map[string]string{}
	for at, bind := range container.binds {
		source, dest, _ := strings.Cut(bind, ":")
		dest, _, _ = strings.Cut(dest, ":")
		held := fmt.Sprintf("2049:%d", 100+at)
		inside[dest] = held
		stats[source] = held
		if at == moved {
			stats[source] = fmt.Sprintf("2049:%d", 900+at)
		}
	}
	return inside, stats
}

func TestAContainerHoldingAMountTheHostNoLongerHasIsDrift(t *testing.T) {
	t.Parallel()

	for _, container := range []boxContainer{frontProxy(), switchboardStanding(nil, Front{})} {
		stated := container.item("").Digest()
		facts := engineSays(container, migrateHeld)
		current, _ := mountedAs(container, -1)
		inside, moved := mountedAs(container, len(container.binds)-1)
		_, held := mountedAs(container, -1)
		for what, probed := range map[string]struct {
			engine  engineReport
			current bool
		}{
			"every mount still the host's own":                      {engineReport{facts: facts, inside: current, stats: held}, true},
			"a mount whose source was replaced, read with no /proc": {engineReport{facts: facts, inside: inside, stats: moved}, false},
			"a container the engine will not exec into":             {engineReport{facts: facts, stats: moved}, true},
			"a container that cannot find the program it is asked":  {engineReport{facts: facts, stats: moved, started: 127}, false},
			"a container that cannot start the program it is asked": {engineReport{facts: facts, stats: moved, started: 126}, false},
			"a source the host will not stat":                       {engineReport{facts: facts, inside: inside}, true},
		} {
			observed := probedAs(t, kernelMigrating(t, true), container, probed.engine)
			if (observed == stated) != probed.current {
				t.Errorf("%s with %s reads as current=%v, want %v: a container reading a mount the host replaced serves what is no longer there, and only a new container picks the new one up",
					container.name, what, observed == stated, probed.current)
			}
		}
	}
}

func TestAContainerHoldingAMountTheHostNoLongerHasIsPlannedBack(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	keys := []byte(aKey + "\n")
	items := Items(class, keys, ArchAMD64, Front{})
	minted := []byte("the key this box minted for itself")
	for _, stood := range []Item{frontItem(), boardItem()} {
		moved := bytes.Replace(stood.Content, []byte(mountsFact+mountsHeld), []byte(mountsFact+mountsMoved), 1)
		if bytes.Equal(moved, stood.Content) {
			t.Fatalf("%s is surveyed without its mounts, so what a moved one proves here is nothing", stood.Name)
		}
		observed := digests(items)
		observed[stood.ID()] = digest(KindContainer, stood.Name, 0, rootOwner, contentSum(moved))
		read := Reading{
			Class: class, Present: true, Keys: keys, Arch: ArchAMD64, Observed: observed,
			Seal: Seal{Fingerprint: contentSum(minted)},
			Stamp: Stamp{
				Schema: providerkit.BootstrapSchema, State: StateComplete,
				Seal: Seal{Fingerprint: contentSum(minted)}, Digests: digests(items),
			},
		}
		if back := planFor(planned(read), stood.ID()); back.Action != providerkit.ActionUpdate {
			t.Errorf("%s reading a mount the host replaced plans %q, want it recreated: the box would call itself current while it serves a directory that is gone", stood.Name, back.Action)
		}
		if read.settled() {
			t.Errorf("a box whose %s reads a mount the host replaced reports itself settled", stood.Name)
		}
	}
}
