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
	spec.Env = map[string]string{
		"DATABASE_URL":                "postgres://app:hunter2@db.internal/orders",
		"API_TOKEN":                   sensitiveValue,
		"REGION":                      "eu-west-1",
		"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"password":"hunter2"}}`,
	}
	return spec
}

func handing(t *testing.T, spec Container) *bench {
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
	stand := handing(t, spec)

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

func TestTheEnvFileIsWrittenAtSixHundredToTheDeployPrincipalWithNoElevation(t *testing.T) {
	t.Parallel()

	spec := valued()
	path := EnvFile(spec.Class, spec.Name)
	stand := handing(t, spec)

	stand.mu.Lock()
	defer stand.mu.Unlock()
	written := ""
	for _, command := range stand.ran {
		if strings.Contains(command, "install") && strings.Contains(command, quoted(path)) {
			written = command
		}
	}
	if written == "" {
		t.Fatalf("nothing wrote %s: %v", path, stand.ran)
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
	stand := handing(t, spec)

	if stand.at("rm -f "+quoted(path)) < 0 {
		t.Fatalf("a deploy that finished left %s standing: %v", path, stand.commands())
	}
	if stand.at("rm -f "+quoted(path)) < stand.at(quoted("--env-file")) {
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

func TestNoValueTheDeployResolvesIsSpokenAnywhereButIntoTheFile(t *testing.T) {
	t.Parallel()

	spec := valued()
	path := EnvFile(spec.Class, spec.Name)
	stand := handing(t, spec)

	file := wrote(t, stand, path)
	for name, value := range spec.Env {
		if !strings.Contains(file, name+"="+value) {
			t.Errorf("the env file reads %q and never binds %s", file, name)
		}
		for _, command := range stand.commands() {
			if strings.Contains(command, value) {
				t.Errorf("%s's value is spoken in %q, and a command a deploy renders reaches its progress output and its plan rows", name, command)
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

func TestAContainerStandingUnderTheSameImageAndOtherValuesIsReplaced(t *testing.T) {
	t.Parallel()

	spec := valued()
	digest, err := envDigest(spec.Env)
	if err != nil {
		t.Fatal(err)
	}
	stand := machine(nil)
	imaging(stand, "running "+appImage+" "+digest)
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

func TestAPromotionThatNamesNoValuesKeepsTheContainerStanding(t *testing.T) {
	t.Parallel()

	spec := aContainer()
	spec.Class = providerkit.ClassProduction
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
