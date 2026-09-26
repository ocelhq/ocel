package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type engine struct {
	installed bool
	unit      bool
	deaf      bool
	active    string
	enabled   string
	server    string
	dockerd   string
	snap      bool
	rootless  bool
	outside   bool
	extras    bool
}

func serving() engine {
	return engine{installed: true, unit: true, active: "active", enabled: "enabled", server: "28.3.1", dockerd: "28.3.1"}
}

func daemon(t *testing.T, fake engine) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		executable(t, filepath.Join(dir, name), "#!/bin/sh\n"+body)
	}
	if fake.installed {
		answer := "echo 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock' >&2; exit 1"
		if fake.server != "" {
			answer = "printf '%s\\n' " + quoted(fake.server)
		}
		write(dockerEngine, `case "$1" in
version) `+answer+` ;;
esac
exit 0`)
		write(dockerDaemon, "printf 'Docker version %s, build 38b7060\\n' "+quoted(fake.dockerd))
	}
	if fake.snap {
		write("snap", `[ "$1" = list ] && [ "$2" = docker ]`)
	}
	write("pgrep", `[ "$1" = -x ] || exit 1
case "$2" in
rootlesskit) `+strconv.FormatBool(fake.rootless)+` ;;
dockerd) `+strconv.FormatBool(fake.outside)+` ;;
*) exit 1 ;;
esac`)
	if fake.extras {
		write("dockerd-rootless.sh", "exit 0")
	}
	known := "exit 1"
	if fake.unit {
		known = "exit 0"
	}
	manager := `case "$1" in
cat) ` + known + ` ;;
is-active) printf '%s\n' ` + quoted(fake.active) + ` ;;
is-enabled) printf '%s\n' ` + quoted(fake.enabled) + ` ;;
list-unit-files) exit 0 ;;
*) exit 1 ;;
esac`
	if fake.deaf {
		manager = "exit 126"
	}
	write("systemctl", manager)
	for _, tool := range []string{"sha256sum", "cut", "timeout"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(found, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func probed(t *testing.T, fake engine) map[string]string {
	t.Helper()
	observed, err := engineProbed(t, fake)
	if err != nil {
		t.Fatal(err)
	}
	return observed
}

func engineProbed(t *testing.T, fake engine) (map[string]string, error) {
	t.Helper()
	observed, _, err := readSurvey(engineRendered(t, fake))
	return observed, err
}

func engineRendered(t *testing.T, fake engine) string {
	t.Helper()
	dir := daemon(t, fake)
	cmd := exec.Command("/bin/sh", "-c", engineProbe()+"\n"+unitProbe(unitItem()))
	cmd.Env = []string{"PATH=" + dir}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	rendered, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe the engine: %v\n%s", err, stderr.String())
	}
	return string(rendered)
}

func engineRead(t *testing.T, fake engine) Engine {
	t.Helper()
	return readEngine(engineRendered(t, fake))
}

func TestTheReadReportsTheEngineVersionBesideItsItemAndNeverInIt(t *testing.T) {
	t.Parallel()

	older, newer := serving(), serving()
	newer.server, newer.dockerd = "29.8.0", "29.8.0"
	if got := engineRead(t, older); got != (Engine{Kind: engineStandard, Version: "28.3.1"}) {
		t.Errorf("a host serving docker 28.3.1 from docker.service reads as %+v", got)
	}
	if probed(t, older)[engineItem().ID()] != probed(t, newer)[engineItem().ID()] {
		t.Error("the engine digests differently at 28.3.1 and at 29.8.0, and every docker upgrade the user makes would read as drift for bootstrap to write over")
	}

	down := serving()
	down.server, down.dockerd = "", "28.0.4"
	if got := engineRead(t, down).Version; got != "28.0.4" {
		t.Errorf("a host whose daemon is down reads docker %q, want the version dockerd itself reports", got)
	}
}

func TestTheReadNamesWhatKindOfDockerTheHostRuns(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		found engine
		want  engineKind
	}{
		"docker's own packages":          {serving(), engineStandard},
		"a masked docker.service":        {engine{installed: true, unit: true, active: "inactive", enabled: "masked", dockerd: "28.3.1"}, engineMasked},
		"the snap package":               {engine{installed: true, snap: true, dockerd: "27.2.0"}, engineSnap},
		"a rootless daemon":              {engine{installed: true, rootless: true, dockerd: "28.3.1"}, engineRootless},
		"a daemon systemd does not run":  {engine{installed: true, outside: true, dockerd: "28.3.1"}, engineUnserved},
		"a binary no daemon runs behind": {engine{installed: true, dockerd: "28.3.1"}, ""},
		"docker's rootless extras, idle": {engine{installed: true, extras: true, dockerd: "28.3.1"}, ""},
		"no docker at all":               {engine{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := engineRead(t, tc.found).Kind; got != tc.want {
				t.Errorf("the read names %q, want %q", got, tc.want)
			}
		})
	}
}

func TestASystemctlThatWillNotRunIsNotAnEngineNothingServesAndNoUnit(t *testing.T) {
	t.Parallel()

	observed, err := engineProbed(t, engine{installed: true, unit: true, deaf: true, active: "active", enabled: "enabled"})
	if err == nil {
		t.Fatalf("a survey whose systemctl could not be run read %v: an engine read as unserved is one apply reinstalls, as root, over the daemon already running every container on the box", observed)
	}
	if !strings.Contains(err.Error(), dockerEngine) {
		t.Errorf("the refusal reads %q and never names what nothing could be read about", err)
	}
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
		if _, present := observed[item.ID()]; present {
			t.Errorf("the probe read %s on a host that has no docker at all", item.ID())
		}
	}
}

func TestADaemonSystemdDoesNotRunIsRefusedRatherThanInstalledOver(t *testing.T) {
	t.Parallel()

	fake := engine{installed: true, outside: true, dockerd: "28.3.1"}
	observed := probed(t, fake)
	if _, present := observed[unitItem().ID()]; present {
		t.Errorf("the probe read %s on a host whose docker binary has no unit file", unitItem().ID())
	}
	read := Reading{Arch: ArchAMD64, Class: edge.ClassProduction, Observed: observed, Engine: engineRead(t, fake)}
	if !read.observed(KindEngine, dockerEngine) {
		t.Fatalf("the probe read no engine on a host with a docker binary, and an apply would fetch %s and run it as root over an install that is already there", dockerSource)
	}
	if read.current(engineItem()) {
		t.Fatalf("a docker binary with no %s reads as serving, and apply would enable a unit that does not exist, on every run, forever", dockerUnit)
	}
	refused := refusalOf(t, read.runnableEngine("ada@ocelbox"), refusal.CodeNotReady)
	if !strings.Contains(refused.Message, dockerUnit) || !strings.Contains(refused.Message, "ocel bootstrap production") {
		t.Errorf("a docker daemon no %s runs is refused with %q, want it to name the unit ocel needs and the bootstrap to run after", dockerUnit, refused.Message)
	}
}

func TestAnInstallLeftHalfDoneIsInstalledAgainRatherThanRefused(t *testing.T) {
	t.Parallel()

	for name, fake := range map[string]engine{
		"docker's cli with no daemon":             {installed: true, dockerd: "28.3.1"},
		"docker's rootless extras with no daemon": {installed: true, extras: true, dockerd: "28.3.1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			read := Reading{Arch: ArchAMD64, Class: edge.ClassProduction, Observed: probed(t, fake), Engine: engineRead(t, fake)}
			if err := read.runnableEngine("ada@ocelbox"); err != nil {
				t.Fatalf("runnableEngine() = %v, and an install interrupted before docker.service landed could never be finished by ocel", err)
			}
			if engine := planFor(planned(read), engineItem().ID()); engine.Action != provider.ActionUpdate {
				t.Errorf("the engine plans %q, want the install shown again as a change over what is installed", engine.Action)
			}
			refused := refuseReplacements(read, EngineItems())
			if refused == nil || !strings.Contains(refused.Error(), engineItem().ID()) {
				t.Errorf("an apply that refuses replacements over a half-done install = %v, want it refused: rebuilding an engine is a replacement that apply never consented to", refused)
			}
		})
	}
}

func TestAnInstalledEngineWhoseDaemonIsIdleIsProbedAsInstalledAndNotCurrent(t *testing.T) {
	t.Parallel()

	for name, fake := range map[string]engine{
		"stopped":         {installed: true, unit: true, active: "inactive", enabled: "enabled"},
		"off at boot":     {installed: true, unit: true, active: "active", enabled: "disabled"},
		"stopped and off": {installed: true, unit: true, active: "inactive", enabled: "disabled"},
		"failed to start": {installed: true, unit: true, active: "failed", enabled: "enabled"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			observed := probed(t, fake)
			if observed[engineItem().ID()] != engineItem().Digest() {
				t.Error("the probe calls an installed engine absent, and the plan would fetch the install script over a host that already has one")
			}
			read := Reading{Arch: ArchAMD64, Class: edge.ClassProduction, Observed: observed}
			if !read.observed(KindUnit, dockerUnit) {
				t.Fatalf("the probe read no unit on a host whose docker.service is %s", name)
			}
			if read.current(unitItem()) {
				t.Errorf("a docker.service that is %s reads as serving, and nothing would ever start it", name)
			}
		})
	}
}

func TestAnInstalledEngineIsAdoptedAndAnIdleDaemonPlansTheUnitAlone(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	idle := digest(KindUnit, dockerUnit, 0, rootOwner, contentSum([]byte("active=inactive\nenabled=disabled\n")))
	read := Reading{Arch: ArchAMD64, Class: class, Engine: Engine{Kind: engineStandard, Version: "28.3.1"}, Observed: map[string]string{
		engineItem().ID(): engineItem().Digest(),
		unitItem().ID():   idle,
	}}

	changes := planned(read)
	engine := planFor(changes, engineItem().ID())
	if engine.Action != provider.ActionAdopt {
		t.Errorf("a host whose engine is installed plans %q for it, want it adopted: an installed engine is never installed over", engine.Action)
	}
	if want := "docker 28.3.1, not managed by ocel: upgrading it is yours"; engine.Reason != want {
		t.Errorf("the adopted engine is planned with the reason %q, want %q: the user is never told the engine is theirs to upgrade", engine.Reason, want)
	}
	if engine.Slow {
		t.Error("an adopted engine is planned as slow work, and adopting it writes nothing at all")
	}
	unit := planFor(changes, unitItem().ID())
	if unit.Action != provider.ActionUpdate {
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

	read := Reading{Arch: ArchAMD64, Class: edge.ClassProduction, Observed: map[string]string{
		engineItem().ID(): engineItem().Digest(),
	}}
	if !read.current(engineItem()) {
		t.Fatal("an engine the probe found is not current, so an installed engine would re-run the install script")
	}
	if !strings.Contains(engineItem().command(), dockerSource) {
		t.Errorf("the engine is installed by %q, want the script the plan names", engineItem().command())
	}
}

func TestTheDocumentSaysWhereTheDaemonTheGroupReachesCameFrom(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	var claim Grant
	for _, grant := range Grants(class) {
		if grant.Name == "membership of the "+dockerGroup+" group" {
			claim = grant
		}
	}
	if claim.Name == "" {
		t.Fatal("the deploy login is written into the docker group and the document claims no membership of it")
	}
	if written(Items(class, nil, ArchAMD64, Front{}), KindEngine, dockerEngine).Name == "" {
		t.Fatal("the document names the daemon bootstrap installs and bootstrap installs no engine at all")
	}
	if !strings.Contains(claim.Detail, dockerSource) {
		t.Errorf("apply installs the engine and the membership claim never says where it came from:\n%s", claim.Detail)
	}
	if !strings.Contains(claim.Detail, "docker "+engineFloor+" or later it finds") {
		t.Errorf("bootstrap adopts a docker it finds and the membership claim says every daemon came from %s:\n%s", dockerSource, claim.Detail)
	}
	if !strings.Contains(claim.Detail, "become root") {
		t.Errorf("the membership claim stopped saying the group is root under another name:\n%s", claim.Detail)
	}
}

func TestAHostWithNoEngineHasTheInstallPlannedLastAndNamed(t *testing.T) {
	t.Parallel()

	changes := planned(Reading{Arch: ArchAMD64, Class: edge.ClassProduction, Observed: map[string]string{}})
	engine := planFor(changes, engineItem().ID())
	if engine.Action != provider.ActionCreate {
		t.Fatalf("a host with no engine plans %q for it, want the install shown as a change to consent to", engine.Action)
	}
	if want := "docker " + dockerVersion + ", installed once; upgrading it is yours from then on"; engine.Reason != want {
		t.Errorf("the engine is planned with the reason %q, want %q: a user consenting to it is never told what runs on their host, nor who upgrades it", engine.Reason, want)
	}
	if !engine.Slow {
		t.Error("the engine install is planned as quick work, and a plan that lies about its cost is one nobody waits through")
	}

	quickest(t, changes)
}

func TestDestroyKeepsTheEngineWhicheverClassIsTheLastOne(t *testing.T) {
	t.Parallel()

	production, preview := edge.ClassProduction, edge.ClassPreview
	keys := []byte(aKey + "\n")
	destroyed := Reading{Arch: ArchAMD64, Class: production, Keys: keys, Observed: digests(Items(production, keys, ArchAMD64, Front{}))}

	for name, sibling := range map[string]Reading{
		"the last class on the host": {Class: preview, Observed: map[string]string{}},
		"a class beside its sibling": {Class: preview, Keys: keys, Observed: digests(Items(preview, keys, ArchAMD64, Front{}))},
	} {
		taken := removing(destroyed, sibling, appsPresent{})
		kept := removalOf(taken, dockerEngine)
		if kept.action != provider.ActionKeep {
			t.Errorf("destroying %s plans %s as %q, want it kept: removing ocel never removes the workloads a host runs",
				name, dockerEngine, kept.action)
		}
		if kept.reason == "" {
			t.Errorf("destroying %s keeps %s and never says why it stays", name, dockerEngine)
		}
		for _, r := range taken {
			if r.action == provider.ActionDelete && r.path == dockerEngine {
				t.Errorf("destroying %s takes %s, and a host loses the engine every container it runs needs", name, r.path)
			}
			if r.path == dockerUnit {
				t.Errorf("destroying %s plans %q over %s, and the unit that starts the engine is not ocel's to touch", name, r.action, dockerUnit)
			}
		}
	}
}

func TestAHostWithNothingButTheEngineHasNothingToDestroy(t *testing.T) {
	t.Parallel()

	production, preview := edge.ClassProduction, edge.ClassPreview
	engine := digests(EngineItems())
	taken := removing(Reading{Arch: ArchAMD64, Class: production, Observed: engine}, Reading{Arch: ArchAMD64, Class: preview, Observed: engine}, appsPresent{})
	if len(taken) != 0 {
		t.Errorf("a machine with nothing but docker plans %d removals, want a destroy with nothing to say", len(taken))
	}
}

func TestSlowWorkClosesThePlanWhateverOrderTheItemsComeIn(t *testing.T) {
	t.Parallel()

	ordered := slowLast([]provider.Change{
		{Name: "first slow", Slow: true},
		{Name: "a directory"},
		{Name: "second slow", Slow: true},
		{Name: "a file"},
	})
	quickest(t, ordered)
	if ordered[0].Name != "a directory" || ordered[1].Name != "a file" {
		t.Errorf("the quick changes are planned as %q and %q, and sorting the slow work last reordered work a user reads top to bottom", ordered[0].Name, ordered[1].Name)
	}
	if ordered[2].Name != "first slow" {
		t.Errorf("the slow work is planned as %q first, and two slow changes swapped places under the sort", ordered[2].Name)
	}
}

func quickest(t *testing.T, changes []provider.Change) {
	t.Helper()
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

func TestAnApplyOverAnAdoptedEngineNeverInstallsDockerAndStillStartsItsUnit(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	box := bootstrappedOn(t, class)
	for at, item := range box.installed[class] {
		if item.ID() == unitItem().ID() {
			box.installed[class][at].Content = []byte("active=inactive\nenabled=disabled\n")
		}
	}
	if err := NewBootstrap(box.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Class: class, WrittenBy: "the-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if at := box.at(dockerSource); at >= 0 {
		t.Errorf("the apply ran the install script over an engine it adopted:\n%s", box.commands()[at])
	}
	if box.at("systemctl enable --now "+quoted(dockerUnit)) < 0 {
		t.Errorf("the apply never started the idle %s the adopted engine runs under:\n%s", dockerUnit, strings.Join(box.commands(), "\n"))
	}
}

func TestAnEngineOcelCannotRunOnIsRefusedWithWhatToDoAboutIt(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		found Engine
		want  string
	}{
		"an engine older than the floor": {Engine{Kind: engineStandard, Version: "26.1.4"},
			"docker 26.1.4 on ada@ocelbox is older than 28.0, the oldest ocel runs on\nUpgrade docker to 28.0 or later and run `ocel bootstrap production`"},
		"the snap package": {Engine{Kind: engineSnap, Version: "27.2.0"},
			"docker on ada@ocelbox is the snap package, which ocel does not run on\nReplace it with docker 28.0 or later from docker's own packages; its containers do not move over"},
		"a rootless daemon": {Engine{Kind: engineRootless, Version: "28.3.1"},
			"docker on ada@ocelbox runs rootless, and ocel needs the system daemon behind docker.service\nInstall docker 28.0 or later as the system daemon and run `ocel bootstrap production`"},
		"a daemon systemd does not run": {Engine{Kind: engineUnserved, Version: "28.3.1"},
			"a docker daemon on ada@ocelbox runs outside docker.service, the system daemon ocel needs\nInstall docker 28.0 or later as the system daemon and run `ocel bootstrap production`"},
		"a masked docker.service": {Engine{Kind: engineMasked, Version: "28.3.1"},
			"docker.service on ada@ocelbox is masked\nRun `systemctl unmask docker.service` and run `ocel bootstrap production`"},
		"an engine whose version cannot be read": {Engine{Kind: engineStandard},
			"docker on ada@ocelbox reports no version ocel can read\nCheck that `docker version` answers as root and run `ocel bootstrap production`"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			read := Reading{Class: edge.ClassProduction, Engine: tc.found}
			if refused := refusalOf(t, read.runnableEngine("ada@ocelbox"), refusal.CodeNotReady); refused.Message != tc.want {
				t.Errorf("the refusal reads\n%s\nwant\n%s", refused.Message, tc.want)
			}
		})
	}
}

func TestAnEngineAtOrPastTheFloorAndNoEngineAtAllAreLetThrough(t *testing.T) {
	t.Parallel()

	for _, allowed := range []Engine{
		{},
		{Kind: engineStandard, Version: "28.0.0"},
		{Kind: engineStandard, Version: "28.0.4"},
		{Kind: engineStandard, Version: "28.3.1"},
		{Kind: engineStandard, Version: "29.8.0"},
		{Kind: engineStandard, Version: "30.0.0-rc.1"},
	} {
		if err := (Reading{Class: edge.ClassProduction, Engine: allowed}).runnableEngine("ada@ocelbox"); err != nil {
			t.Errorf("an engine read as %+v = %v, want it let through", allowed, err)
		}
	}
	if err := (Reading{Class: edge.ClassProduction, Engine: Engine{Kind: engineStandard, Version: "27.5.1"}}).runnableEngine("ada@ocelbox"); err == nil {
		t.Error("docker 27.5.1 is let through, one minor release below the floor")
	}
}

func reportingEngine(box *bench, reported Engine) {
	prior := box.answer
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "for p in") {
			said := box.rendered(command)
			said.Stdout = strings.ReplaceAll(said.Stdout,
				engineFactsRow+"\t"+dockerEngine+"\t0\t"+string(engineStandard)+"\t28.3.1\n",
				engineFactsRow+"\t"+dockerEngine+"\t0\t"+string(reported.Kind)+"\t"+reported.Version+"\n")
			return said, true
		}
		if prior != nil {
			return prior(command)
		}
		return session.Result{}, false
	}
}

func TestABootstrapOverAnEngineOcelCannotRunOnStopsBeforeItsFirstWrite(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	box := bootstrappedOn(t, class)
	box.installed[class] = slices.DeleteFunc(box.installed[class], func(item Item) bool { return item.Name == ClassDir(class) })
	reportingEngine(box, Engine{Kind: engineStandard, Version: "26.1.4"})

	boot := NewBootstrap(box.host(), testVendor, "shop")
	_, planned := boot.Plan(context.Background(), provider.BootstrapRequest{Class: class})
	applied := boot.Apply(context.Background(), provider.BootstrapRequest{Class: class, WrittenBy: "the-suite"}, nil)
	for step, err := range map[string]error{"Plan": planned, "Apply": applied} {
		if refused := refusalOf(t, err, refusal.CodeNotReady); !strings.Contains(refused.Message, "docker 26.1.4") {
			t.Errorf("%s refused with %q, want it to name the docker it will not run on", step, refused.Message)
		}
	}
	for _, command := range box.commands() {
		if strings.HasPrefix(command, "install ") || strings.Contains(command, dockerSource) {
			t.Errorf("the refused bootstrap still wrote: %s", command)
		}
	}
}

func TestAMaskedDockerServiceStopsThePlanRatherThanWedgingItOnAnEnableThatNeverTakes(t *testing.T) {
	t.Parallel()

	for name, fake := range map[string]engine{
		"with docker installed": {installed: true, unit: true, active: "inactive", enabled: "masked", dockerd: "28.3.1"},
		"with no docker at all": {unit: true, active: "inactive", enabled: "masked"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			read := Reading{Arch: ArchAMD64, Class: edge.ClassProduction, Observed: probed(t, fake), Engine: engineRead(t, fake)}
			refused := refusalOf(t, read.runnableEngine("ada@ocelbox"), refusal.CodeNotReady)
			if !strings.Contains(refused.Message, "systemctl unmask "+dockerUnit) {
				t.Errorf("a masked %s is refused with %q, want the unmask to run named: apply would otherwise run `systemctl enable --now` against it on every run, forever", dockerUnit, refused.Message)
			}
		})
	}
}

func TestAWriteThatFailsIsDeniedOnlyWhenSudoRefusedTheLogin(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		root bool
		said string
		want refusal.Code
	}{
		"sudo asking a password":             {said: "sudo: a password is required", want: refusal.CodeDenied},
		"a login sudoers never names":        {said: "ada is not in the sudoers file.  This incident will be reported.", want: refusal.CodeDenied},
		"a command sudoers does not grant":   {said: "Sorry, user ada is not allowed to execute '/bin/sh -c install' as root on ocelbox.", want: refusal.CodeDenied},
		"a path the elevated write can't be": {said: "install: cannot create directory '/srv/ocel': Permission denied", want: refusal.CodeNotReady},
		"a runtime the kernel refuses":       {said: "docker: Error response from daemon: failed to create task for container: operation not permitted", want: refusal.CodeNotReady},
		"an install whose mirror stalled":    {said: "E: Failed to fetch https://download.docker.com/linux/ubuntu/dists/noble/InRelease  Connection timed out\nE: Some index files failed to download.", want: refusal.CodeNotReady},
		"root, which sudo never elevated":    {root: true, said: "sudo: a password is required", want: refusal.CodeNotReady},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			box := machine(nil)
			box.facts.Root, box.facts.Sudo = tc.root, !tc.root
			box.answer = func(command string) (session.Result, bool) {
				return session.Result{Code: 1, Stderr: tc.said}, strings.Contains(command, "install -d")
			}
			item := Item{Kind: KindDir, Name: "/srv/ocel", Mode: 0o755, Owner: rootOwner}
			if refused := refusalOf(t, box.host().Install(context.Background(), item), tc.want); !strings.Contains(refused.Message, strings.Split(tc.said, "\n")[0]) {
				t.Errorf("the refusal reads %q, want what the host said in it", refused.Message)
			}
		})
	}
}
