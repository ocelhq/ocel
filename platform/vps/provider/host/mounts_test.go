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
)

type engineReport struct {
	facts string
	pid   string
	stats map[string]string
}

func probedAs(t *testing.T, k kernel, container boxContainer, engine engineReport) string {
	t.Helper()
	dir := t.TempDir()
	said := filepath.Join(dir, "facts")
	if err := os.WriteFile(said, []byte(engine.facts), 0o600); err != nil {
		t.Fatal(err)
	}
	pid := "[ -n " + quoted(engine.pid) + " ] || exit 1; echo " + quoted(engine.pid)
	executable(t, filepath.Join(dir, dockerEngine), "#!/bin/sh\n"+
		"[ \"$1\" = inspect ] || exit 1\n"+
		"case \"$*\" in *State.Pid*) "+pid+" ;; *) cat "+quoted(said)+" ;; esac\n")
	stat := "#!/bin/sh\nfor last; do :; done\ncase \"$last\" in\n"
	for path, held := range engine.stats {
		stat += quoted(path) + ") echo " + quoted(held) + " ;;\n"
	}
	executable(t, filepath.Join(dir, "stat"), stat+"*) echo \"stat: cannot statx '$last': Permission denied\" >&2; exit 1 ;;\nesac\n")
	for _, tool := range []string{"sha256sum", "cut", "cat", "sort"} {
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

func mountedAs(container boxContainer, pid string, moved int) map[string]string {
	stats := map[string]string{}
	for at, bind := range container.binds {
		source, dest, _ := strings.Cut(bind, ":")
		dest, _, _ = strings.Cut(dest, ":")
		held := fmt.Sprintf("2049:%d", 100+at)
		stats[source] = held
		if at == moved {
			stats[source] = fmt.Sprintf("2049:%d", 900+at)
		}
		if pid != "" {
			stats["/proc/"+pid+"/root"+dest] = held
		}
	}
	return stats
}

func TestAContainerHoldingAMountTheHostNoLongerHasIsDrift(t *testing.T) {
	t.Parallel()

	for _, container := range []boxContainer{frontProxy(), switchboardStanding(nil)} {
		stated := container.item("").Digest()
		facts := engineSays(container, migrateHeld)
		for what, held := range map[string]struct {
			engine  engineReport
			current bool
		}{
			"every mount still the host's own":  {engineReport{facts: facts, pid: "4242", stats: mountedAs(container, "4242", -1)}, true},
			"a mount whose source was replaced": {engineReport{facts: facts, pid: "4242", stats: mountedAs(container, "4242", len(container.binds)-1)}, false},
			"a /proc the probe cannot read":     {engineReport{facts: facts, pid: "4242", stats: mountedAs(container, "", len(container.binds)-1)}, true},
			"no pid the engine will name":       {engineReport{facts: facts, stats: mountedAs(container, "", 0)}, true},
		} {
			observed := probedAs(t, kernelMigrating(t, true), container, held.engine)
			if (observed == stated) != held.current {
				t.Errorf("%s with %s reads as current=%v, want %v: a container reading a mount the host replaced serves what is no longer there, and only a new container picks the new one up",
					container.name, what, observed == stated, held.current)
			}
		}
	}
}

func TestAContainerHoldingAMountTheHostNoLongerHasIsPlannedBack(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	items := Items(class, keys, ArchAMD64)
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
