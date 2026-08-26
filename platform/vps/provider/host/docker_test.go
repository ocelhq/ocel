package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type engine struct {
	installed bool
	unit      bool
	active    string
	enabled   string
}

func serving() engine {
	return engine{installed: true, unit: true, active: "active", enabled: "enabled"}
}

func daemon(t *testing.T, held engine) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if held.installed {
		write(dockerEngine, "exit 0")
	}
	known := "exit 1"
	if held.unit {
		known = "exit 0"
	}
	write("systemctl", `case "$1" in
cat) `+known+` ;;
is-active) printf '%s\n' `+quoted(held.active)+` ;;
is-enabled) printf '%s\n' `+quoted(held.enabled)+` ;;
*) exit 1 ;;
esac`)
	for _, tool := range []string{"sha256sum", "cut"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("this machine has no %s to answer a survey with: %v", tool, err)
		}
		if err := os.Symlink(found, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func probed(t *testing.T, held engine) map[string]string {
	t.Helper()
	dir := daemon(t, held)
	observed, _, err := readSurvey(sh(t, dir, "PATH="+quoted(dir)+"\n"+engineProbe()+"\n"+unitProbe()))
	if err != nil {
		t.Fatal(err)
	}
	return observed
}

func TestTheProbeAndTheWriteAgreeOnWhatAServingEngineIs(t *testing.T) {
	t.Parallel()

	observed := probed(t, serving())
	for _, item := range EngineItems() {
		if observed[item.ID()] != item.Digest() {
			t.Errorf("the probe read %q of %s on a host that serves containers, want %q", observed[item.ID()], item.ID(), item.Digest())
		}
	}
}

func TestAHostThatRunsNoContainersIsProbedAsHavingNeither(t *testing.T) {
	t.Parallel()

	observed := probed(t, engine{})
	for _, item := range EngineItems() {
		if _, stood := observed[item.ID()]; stood {
			t.Errorf("the probe read %s on a host that has no docker at all", item.ID())
		}
	}
}

func TestAnInstalledEngineWhoseDaemonIsIdleIsProbedAsStandingAndNotCurrent(t *testing.T) {
	t.Parallel()

	for name, held := range map[string]engine{
		"stopped":         {installed: true, unit: true, active: "inactive", enabled: "enabled"},
		"off at boot":     {installed: true, unit: true, active: "active", enabled: "disabled"},
		"stopped and off": {installed: true, unit: true, active: "inactive", enabled: "disabled"},
		"failed to start": {installed: true, unit: true, active: "failed", enabled: "enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			observed := probed(t, held)
			if observed[engineItem().ID()] != engineItem().Digest() {
				t.Error("the probe calls an installed engine absent, and the plan would fetch the install script over a host that already has one")
			}
			read := Reading{Class: providerkit.ClassProduction, Observed: observed}
			if !read.standing(KindUnit, dockerUnit) {
				t.Fatalf("the probe read no unit on a host whose docker.service is %s", name)
			}
			if read.current(unitItem()) {
				t.Errorf("a docker.service that is %s reads as serving, and nothing would ever start it", name)
			}
		})
	}
}

func TestAnEngineThatStandsIsKeptAndAnIdleDaemonPlansTheUnitAlone(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	idle := digest(KindUnit, dockerUnit, 0, rootOwner, contentSum([]byte("active=inactive\nenabled=disabled\n")))
	read := Reading{Class: class, Observed: map[string]string{
		engineItem().ID(): engineItem().Digest(),
		unitItem().ID():   idle,
	}}

	changes := planned(read)
	if engine := planFor(changes, engineItem().ID()); engine.Action != providerkit.ActionKeep {
		t.Errorf("a host whose engine stands plans %q for it, want it kept: an installed engine is never installed over", engine.Action)
	}
	unit := planFor(changes, unitItem().ID())
	if unit.Action != providerkit.ActionUpdate {
		t.Errorf("a host whose daemon is idle plans %q for the unit, want it brought to serving", unit.Action)
	}
	if strings.Contains(unit.Reason, dockerSource) {
		t.Errorf("the idle daemon is remediated by %q, and an engine already on the host is never fetched again", unit.Reason)
	}
	if !strings.Contains(unitItem().command(), "systemctl enable --now") {
		t.Errorf("the unit is written with %q, want the unit enabled and started", unitItem().command())
	}
}

func TestTheEngineCanOnlyEverBePresentOrAbsent(t *testing.T) {
	t.Parallel()

	stood := Reading{Class: providerkit.ClassProduction, Observed: map[string]string{
		engineItem().ID(): engineItem().Digest(),
	}}
	if !stood.current(engineItem()) {
		t.Fatal("an engine the probe found is not current, so a standing engine would re-run the install script")
	}
	if !strings.Contains(engineItem().command(), dockerSource) {
		t.Errorf("the engine is installed by %q, want the script the plan names", engineItem().command())
	}
}

func TestTheDocumentSaysWhereTheDaemonTheGroupReachesCameFrom(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	var claim Grant
	for _, grant := range Grants(class) {
		if grant.Name == "membership of the "+dockerGroup+" group" {
			claim = grant
		}
	}
	if claim.Name == "" {
		t.Fatal("the deploy login is written into the docker group and the document claims no membership of it")
	}
	installs := written(Items(class, nil), dockerEngine).Kind == KindEngine
	if installs != strings.Contains(claim.Detail, dockerSource) {
		t.Errorf("apply installs the engine: %v, and the document says so: %v\n%s", installs, !installs, claim.Detail)
	}
	if !strings.Contains(claim.Detail, "become root") {
		t.Errorf("the membership claim stopped saying the group is root under another name:\n%s", claim.Detail)
	}
}

func TestDestroyTakesNeitherTheEngineNorTheDaemonWithIt(t *testing.T) {
	t.Parallel()

	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	keys := []byte(aKey + "\n")
	held := digests(Items(production, keys))
	for _, taken := range removing(
		Reading{Class: production, Keys: keys, Observed: held},
		Reading{Class: preview, Observed: map[string]string{}},
	) {
		if taken.kind == KindEngine || taken.kind == KindUnit {
			t.Errorf("destroy takes %s %s, and removing ocel from a host must never remove the workloads on it", taken.kind, taken.path)
		}
	}
}

func TestAHostWithNoEngineHasTheInstallPlannedLastAndNamed(t *testing.T) {
	t.Parallel()

	changes := planned(Reading{Class: providerkit.ClassProduction, Observed: map[string]string{}})
	engine := planFor(changes, engineItem().ID())
	if engine.Action != providerkit.ActionCreate {
		t.Fatalf("a host with no engine plans %q for it, want the install shown as a change to consent to", engine.Action)
	}
	if !strings.Contains(engine.Reason, "get.docker.com") {
		t.Errorf("the engine is planned with the reason %q, and a user consenting to it is never told what runs on their host", engine.Reason)
	}
	if !engine.Slow {
		t.Error("the engine install is planned as quick work, and a plan that lies about its cost is one nobody waits through")
	}

	var slow bool
	for _, change := range changes {
		if change.Slow {
			slow = true
			continue
		}
		if slow {
			t.Errorf("%s is planned after slow work, and the quick changes are what a watching user sees land first", change.Name)
		}
	}
}
