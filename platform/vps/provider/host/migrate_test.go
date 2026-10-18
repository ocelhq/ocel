package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
)

type kernel struct{ knob string }

func kernelMigrating(t *testing.T, can bool) kernel {
	t.Helper()
	knob := filepath.Join(t.TempDir(), "tcp_migrate_req")
	if can {
		if err := os.WriteFile(knob, []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return kernel{knob: knob}
}

func (k kernel) script(written string) string {
	return strings.ReplaceAll(written, docker.MigrateKnob, k.knob)
}

func ranWith(t *testing.T, k kernel, container docker.Service) string {
	t.Helper()
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	executable(t, filepath.Join(dir, dockerEngine), "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"run|create) printf '%s\\n' \"$*\" >> "+shell.Quote(runs)+" ;;\n"+
		"inspect) echo running ;;\n"+
		"esac\nexit 0\n")
	container.Files = nil
	if said, err := writing(t, dir, k.script(container.Writing(1))); err != nil {
		t.Fatalf("the write of %s = %v\n%s", container.Name, err, said)
	}
	read, err := os.ReadFile(runs)
	if err != nil {
		t.Fatal(err)
	}
	return string(read)
}

func TestTheFrontProxyHandsItsListenersOnWhereverTheKernelCan(t *testing.T) {
	t.Parallel()

	migrating := "--sysctl " + docker.MigrateSysctl + "=1"
	if run := ranWith(t, kernelMigrating(t, true), frontProxy()); !strings.Contains(run, migrating) {
		t.Errorf("the front proxy on a kernel that migrates listeners ran as %q, with no %s: a reload then resets the connections queued on the listener it closes", run, migrating)
	}
	if run := ranWith(t, kernelMigrating(t, false), frontProxy()); strings.Contains(run, "--sysctl") {
		t.Errorf("the front proxy on a kernel with no %s ran as %q: the engine refuses a sysctl the kernel lacks, and the box would serve nothing", docker.MigrateSysctl, run)
	}
	if run := ranWith(t, kernelMigrating(t, true), switchboardBox(nil, front.Proxy{})); strings.Contains(run, "--sysctl") {
		t.Errorf("the switchboard ran as %q; it never reloads, so it has no listener to hand on", run)
	}
}

func TestAFrontProxyIsCurrentWhenItMigratesItsListenersOrItsKernelCannot(t *testing.T) {
	t.Parallel()

	front := frontProxy()
	stated := containerItem(front, "").Digest()
	for what, given := range map[string]struct {
		can     bool
		migrate string
		current bool
	}{
		"migrating on a kernel that can":        {can: true, migrate: docker.MigrateSet, current: true},
		"not migrating on a kernel that can":    {can: true, migrate: "unset", current: false},
		"not migrating on a kernel that cannot": {can: false, migrate: "unset", current: true},
		"migrating on a kernel that cannot":     {can: false, migrate: docker.MigrateSet, current: true},
	} {
		observed := probedAs(t, kernelMigrating(t, given.can), front, engineReport{facts: engineSays(front, given.migrate)})
		if (observed == stated) != given.current {
			t.Errorf("a front proxy %s reads as current=%v, want %v", what, observed == stated, given.current)
		}
	}
}
