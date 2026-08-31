package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	secretValue    = "postgres://app:hunter2@db.internal/orders"
	sensitiveValue = "sk-live-0123456789"
)

func valued() Container {
	spec := aContainer()
	spec.Class = providerkit.ClassProduction
	spec.Resolved = true
	spec.Env = map[string]string{
		"DATABASE_URL":                secretValue,
		"API_TOKEN":                   sensitiveValue,
		"REGION":                      "eu-west-1",
		"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"password":"hunter2"}}`,
	}
	return spec
}

func promoted() Container {
	spec := aContainer()
	spec.Class = providerkit.ClassProduction
	spec.Resolved = false
	return spec
}

func standingWith(t *testing.T, spec Container) *bench {
	t.Helper()
	stand := machine(nil)
	imaging(stand, "false ")
	if err := stand.host().StandUp(context.Background(), spec); err != nil {
		t.Fatalf("StandUp() = %v", err)
	}
	return stand
}

func wrote(t *testing.T, stand *bench, path string) string {
	t.Helper()
	stand.mu.Lock()
	defer stand.mu.Unlock()
	for at, command := range stand.ran {
		if strings.Contains(command, "install") && strings.Contains(command, quoted(path)) {
			return stand.fed[at]
		}
	}
	t.Fatalf("nothing wrote %s: %v", path, stand.ran)
	return ""
}

func TestAContainerIsHandedItsValuesInAFileRatherThanOnTheCommandLine(t *testing.T) {
	t.Parallel()

	spec := valued()
	path := EnvFile(spec.Class, spec.Name)
	stand := standingWith(t, spec)

	command := ranContainer(t, stand)
	if !strings.Contains(command, quoted("--env-file")+" "+quoted(path)) {
		t.Fatalf("standing a container up runs %q, which hands it no env file", command)
	}
	if !strings.HasPrefix(path, StateDir(spec.Class)+"/") {
		t.Errorf("the env file stands at %q, want it under the state directory of the class whose key sealed what is in it", path)
	}
	for name, value := range spec.Env {
		if strings.Contains(command, value) {
			t.Errorf("the command line standing a container up carries %s's value, which every login on this box reads out of `ps` for as long as the deploy runs", name)
		}
	}
}

func denyingDocker(stand *bench, held string) {
	stand.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, dockerReach) {
			return session.Result{Code: 1, Stderr: "permission denied while trying to connect to the Docker daemon socket"}, true
		}
		if strings.Contains(command, "docker inspect") && strings.Contains(command, quoted(servingSelectors())) {
			return session.Result{Stdout: held + "\n"}, true
		}
		return session.Result{}, false
	}
}

func TestTheEnvFileIsWrittenAtSixHundredToTheDeployPrincipalWithNoElevation(t *testing.T) {
	t.Parallel()

	spec := valued()
	path := EnvFile(spec.Class, spec.Name)
	stand := machine(nil)
	stand.facts.Root = false
	denyingDocker(stand, "false ")
	if err := stand.host().StandUp(context.Background(), spec); err != nil {
		t.Fatalf("StandUp() = %v", err)
	}

	run := ""
	for _, command := range stand.commands() {
		if strings.Contains(command, "--detach") && strings.Contains(command, "docker") {
			run = command
		}
	}
	if run == "" {
		t.Fatalf("nothing stood a container up: %v", stand.commands())
	}
	if !strings.Contains(run, "sudo -n sh -c ") {
		t.Fatalf("a login this bench gives no docker access to ran %q with no elevation, so this bench cannot tell an elevated command from a bare one and nothing it says about the env file's own elevation means anything", run)
	}

	written := ""
	for _, command := range stand.commands() {
		if strings.Contains(command, "install") && strings.Contains(command, quoted(path)) {
			written = command
		}
	}
	if written == "" {
		t.Fatalf("nothing wrote %s: %v", path, stand.commands())
	}
	if !strings.Contains(written, "install -m 0600") {
		t.Errorf("the env file is written by %q, and every value an app holds is readable by whoever the mode admits", written)
	}
	if strings.Contains(written, "sudo") {
		t.Errorf("the env file is written by %q: the deploy login can sudo nothing but the seal helper, and a file written as root is not the deploy principal's", written)
	}
	if strings.Contains(written, "-o ") || strings.Contains(written, "chown") {
		t.Errorf("the env file is written by %q, which names an owner rather than being written as the login that writes it", written)
	}
}

func TestTheEnvFileIsTakenBackOnTheSuccessPath(t *testing.T) {
	t.Parallel()

	spec := valued()
	path := EnvFile(spec.Class, spec.Name)
	stand := standingWith(t, spec)

	taken, ran := stand.at("rm -f "+quoted(path)), stand.at(quoted("--env-file"))
	if taken < 0 {
		t.Fatalf("a deploy that finished left %s standing: %v", path, stand.commands())
	}
	if ran < 0 {
		t.Fatalf("nothing ran a container off %s, so the order the two happen in proves nothing: %v", path, stand.commands())
	}
	if taken < ran {
		t.Error("the env file is removed before the container that reads it is run")
	}
}

func TestTheEnvFileIsTakenBackWhenTheContainerCannotBeStoodUp(t *testing.T) {
	t.Parallel()

	spec := valued()
	path := EnvFile(spec.Class, spec.Name)
	stand := machine(nil)
	stand.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker inspect") && strings.Contains(command, quoted(servingSelectors())) {
			return session.Result{Stdout: "false \n"}, true
		}
		if strings.Contains(command, quoted("--detach")) {
			return session.Result{Code: 125, Stderr: "no such image"}, true
		}
		return session.Result{}, false
	}
	err := stand.host().StandUp(context.Background(), spec)
	if err == nil {
		t.Fatal("StandUp() over a daemon that refused the run = nil")
	}
	if stand.at("rm -f "+quoted(path)) < 0 {
		t.Fatalf("a deploy that fell over left %s standing with every value in it: %v", path, stand.commands())
	}
	for name, value := range spec.Env {
		if strings.Contains(err.Error(), value) {
			t.Errorf("the refusal a failed stand-up returns carries %s's value", name)
		}
	}
}

func TestTheEnvFileIsTakenBackWhenTheWriteItselfFallsOver(t *testing.T) {
	t.Parallel()

	spec := valued()
	path := EnvFile(spec.Class, spec.Name)
	stand := machine(nil)
	stand.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker inspect") && strings.Contains(command, quoted(servingSelectors())) {
			return session.Result{Stdout: "false \n"}, true
		}
		if strings.Contains(command, "install -m 0600") {
			return session.Result{Code: 1, Stderr: "no space left on device"}, true
		}
		return session.Result{}, false
	}
	if err := stand.host().StandUp(context.Background(), spec); err == nil {
		t.Fatal("StandUp() over a write that fell over = nil")
	}
	if stand.at("install -m 0600") < 0 {
		t.Fatalf("nothing tried to write %s, so this bench proves nothing about a write that fell over: %v", path, stand.commands())
	}
	if stand.at("rm -f "+quoted(path)) < 0 {
		t.Fatalf("a write that fell over left %s to whatever `install` had already put on disk, and no deploy after this one takes it back: %v", path, stand.commands())
	}
}

func TestTheEnvFileIsTakenBackWhenTheDeployIsInterrupted(t *testing.T) {
	t.Parallel()

	spec := valued()
	path := EnvFile(spec.Class, spec.Name)
	stand := machine(nil)
	imaging(stand, "false ")
	ctx, stop := context.WithCancel(context.Background())
	stand.after = func(_ *bench, command string) {
		if strings.Contains(command, "install -m 0600") {
			stop()
		}
	}
	if err := stand.host().StandUp(ctx, spec); err == nil {
		t.Fatal("StandUp() over a deploy interrupted after the write = nil")
	}
	if stand.at("install -m 0600") < 0 {
		t.Fatalf("nothing wrote %s before the interrupt, so what follows it proves nothing: %v", path, stand.commands())
	}
	if stand.at("rm -f "+quoted(path)) < 0 {
		t.Fatalf("a deploy interrupted after the write left %s standing with every value in it: %v", path, stand.commands())
	}
}

func TestNoValueTheDeployResolvesIsSpokenAnywhereButIntoTheFile(t *testing.T) {
	t.Parallel()

	spec := valued()
	path := EnvFile(spec.Class, spec.Name)
	stand := standingWith(t, spec)

	file := wrote(t, stand, path)
	for name, value := range spec.Env {
		if !strings.Contains(file, name+"="+value) {
			t.Errorf("the env file reads %q and never binds %s", file, name)
		}
		for _, command := range stand.commands() {
			if strings.Contains(command, value) {
				t.Errorf("%s's value is spoken in %q, which is a line this deploy puts on the wire and every login on this box reads out of `ps`", name, command)
			}
		}
	}
}

func TestTheEnvFileIsRenderedInOneOrderWhateverOrderItIsBuiltIn(t *testing.T) {
	t.Parallel()

	first, err := RenderEnvFile(map[string]string{"B": "2", "A": "1", "C": "3"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderEnvFile(map[string]string{"C": "3", "A": "1", "B": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("two renders of one environment read %q and %q, and nothing downstream could tell a changed value from a reordered map", first, second)
	}
	if string(first) != "A=1\nB=2\nC=3\n" {
		t.Errorf("the env file reads %q, want one KEY=VALUE line per value", first)
	}
}

func TestAValueNoEnvFileLineCanCarryIsRefusedRatherThanTruncated(t *testing.T) {
	t.Parallel()

	for what, env := range map[string]map[string]string{
		"a value carrying a line break":   {"MOTD": "one\ntwo"},
		"a value carrying a return":       {"MOTD": "one\rtwo"},
		"a name carrying the separator":   {"A=B": "one"},
		"a name a parser reads as a note": {"#A": "one"},
		"a name with nothing in it":       {"": "one"},
	} {
		if _, err := RenderEnvFile(env); err == nil {
			t.Errorf("%s was rendered rather than refused, and docker reads an env file one line at a time", what)
		}
	}
}

func TestTheLabelAContainerCarriesTellsOneAppsValuesFromAnothers(t *testing.T) {
	t.Parallel()

	spec := valued()
	held, err := handing(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(held.digest) != envDigestLen {
		t.Errorf("a container is labelled %q, want %d characters: the label is read back off a running container and compared as a whole", held.digest, envDigestLen)
	}

	elsewhere := spec
	elsewhere.Name = spec.Name + "-two"
	other, err := handing(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if other.digest == held.digest {
		t.Errorf("two containers holding the same values are both labelled %q, and whoever reads the labels alone on this box or another learns that two apps hold one value set", held.digest)
	}

	empty, err := handing(promoted())
	if err != nil {
		t.Fatal(err)
	}
	if empty.digest == "" {
		t.Error("an app that resolved no value at all is labelled with nothing, and a container standing under the values a since-emptied deploy handed it would read as still serving")
	}
}

func TestAContainerStandingUnderTheSameImageAndOtherValuesIsReplaced(t *testing.T) {
	t.Parallel()

	spec := valued()
	held, err := handing(spec)
	if err != nil {
		t.Fatal(err)
	}
	stand := machine(nil)
	imaging(stand, "running "+appImage+" "+held.digest)
	if err := stand.host().StandUp(context.Background(), spec); err != nil {
		t.Fatalf("StandUp() = %v", err)
	}
	for _, command := range stand.commands() {
		if strings.Contains(command, quoted("run")+" "+quoted("--detach")) {
			t.Errorf("a redeploy of the same image and the same values ran %q and tore down a container serving live traffic", command)
		}
	}

	moved := machine(nil)
	imaging(moved, "running "+appImage+" 0000000000000000")
	if err := moved.host().StandUp(context.Background(), spec); err != nil {
		t.Fatalf("StandUp() = %v", err)
	}
	if ranContainer(t, moved) == "" {
		t.Error("a redeploy that changed a value alone kept the container holding the old one")
	}
}

func TestADeployThatResolvedNoValueReplacesAContainerHoldingTheOnesItDropped(t *testing.T) {
	t.Parallel()

	stood := valued()
	held, err := handing(stood)
	if err != nil {
		t.Fatal(err)
	}
	emptied := stood
	emptied.Env = nil

	stand := machine(nil)
	imaging(stand, "running "+appImage+" "+held.digest)
	if err := stand.host().StandUp(context.Background(), emptied); err != nil {
		t.Fatalf("StandUp() = %v", err)
	}
	joined := strings.Join(stand.commands(), "\n")
	if !strings.Contains(joined, quoted("run")+" "+quoted("--detach")) {
		t.Fatalf("a deploy that resolved no value at all kept the container standing with the values the deploy before it handed over, so removing the last value an app declares never reaches what serves it and DATABASE_URL goes on being served for the life of the release:\n%s", joined)
	}
	if strings.Contains(joined, "--env-file") {
		t.Errorf("a deploy that resolved no value handed the container an env file:\n%s", joined)
	}
}

func TestAPromotionKeepsTheContainerItRePointsAtRatherThanReRenderingIt(t *testing.T) {
	t.Parallel()

	spec := promoted()
	spec.Declared = []string{"API_TOKEN", "DATABASE_URL"}
	stand := machine(nil)
	imaging(stand, "running "+appImage+" 7f3a9c1e2b4d")
	if err := stand.host().StandUp(context.Background(), spec); err != nil {
		t.Fatalf("StandUp() = %v", err)
	}
	for _, command := range stand.commands() {
		if strings.Contains(command, quoted("run")+" "+quoted("--detach")) {
			t.Errorf("re-pointing the proxy at a standing container ran %q, and the values it was stood up with are not the promotion's to re-render", command)
		}
	}
}

func TestAPromotionIsRefusedByNameRatherThanStandingAnAppUpWithNoValues(t *testing.T) {
	t.Parallel()

	spec := promoted()
	spec.Declared = []string{"API_TOKEN", "DATABASE_URL"}
	stand := machine(nil)
	imaging(stand, "false ")

	err := stand.host().StandUp(context.Background(), spec)
	if err == nil {
		t.Fatalf("a promotion that had to put %s back stood it up carrying none of the values it declares: %v", spec.Name, stand.commands())
	}
	for _, want := range []string{spec.App, "API_TOKEN", "DATABASE_URL", "ocel deploy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads %q and never names %q", err, want)
		}
	}
	for _, command := range stand.commands() {
		if strings.Contains(command, quoted("run")+" "+quoted("--detach")) {
			t.Errorf("a refused promotion still ran %q", command)
		}
	}
}

func TestAPromotionOfAnAppDeclaringNoValueStandsItBackUp(t *testing.T) {
	t.Parallel()

	stand := machine(nil)
	imaging(stand, "false ")
	if err := stand.host().StandUp(context.Background(), promoted()); err != nil {
		t.Fatalf("StandUp() of an app that declares no value = %v, want a rollback of it to stand it back up", err)
	}
	if ranContainer(t, stand) == "" {
		t.Error("a promotion of an app that declares no value left it down")
	}
}

func TestAPromotionStartsTheStoppedContainerItRePointsAtRatherThanRefusingIt(t *testing.T) {
	t.Parallel()

	spec := promoted()
	spec.Declared = []string{"API_TOKEN", "DATABASE_URL"}
	stand := machine(nil)
	imaging(stand, "exited "+appImage+" 7f3a9c1e2b4d")
	if err := stand.host().StandUp(context.Background(), spec); err != nil {
		t.Fatalf("StandUp() of a stopped container = %v, want it started back up: a deploy stops the container it replaces and never removes it, so a rollback re-points at the one that still holds the values its own deploy handed it", err)
	}
	joined := strings.Join(stand.commands(), "\n")
	if !strings.Contains(joined, "docker start "+quoted(spec.Name)) {
		t.Errorf("a promotion of a stopped container ran\n%s\nand never started it", joined)
	}
	for _, unwanted := range []string{quoted("run") + " " + quoted("--detach"), "docker rm"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("a promotion of a stopped container ran %q, and re-creating it is what loses every value that container was handed:\n%s", unwanted, joined)
		}
	}
}
