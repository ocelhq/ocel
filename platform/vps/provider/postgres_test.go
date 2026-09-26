package vps_test

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const standingPassword = "5f1c0d7e9a3b48264c5d6e7f8091a2b3c4d5e6f708192a3b"

func aPostgres(t *testing.T, version string) resources.Instruction {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return resources.Instruction{
		Ref: provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: stack},
		Resource: provider.Resource{
			Name: "main", Type: provider.BindingPostgres,
			Postgres: &provider.PostgresSpec{Version: version},
		},
	}
}

func holdingAPostgres(machine *box) {
	sealed := fakeSeal + base64.StdEncoding.EncodeToString([]byte(standingPassword))
	machine.kept = base64.StdEncoding.EncodeToString([]byte(sealed)) + "\n"
}

func TestAProviderOverABoxServesPostgres(t *testing.T) {
	t.Parallel()

	if served := over(&box{}).Facts().Bindings; !slices.Contains(served, provider.BindingPostgres) {
		t.Errorf("Serves() = %v, and a project declaring a postgres is refused at deploy on a box", served)
	}
}

func TestADeclaredPostgresStandsUpAsOneContainerOnlyItsProjectReaches(t *testing.T) {
	t.Parallel()

	machine := &box{}
	binding, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil)
	if err != nil {
		t.Fatalf("Postgres() = %v", err)
	}
	if err := provider.VerifyProperties(binding); err != nil {
		t.Fatalf("the binding that came back is one no app can bind a client to: %v", err)
	}
	if binding.Name != "main" || binding.Type != provider.BindingPostgres {
		t.Errorf("the binding is %s %q, want the postgres the project declared as main", binding.Type, binding.Name)
	}
	if got := binding.Properties[provider.PropertyDatabase]; got != "main" {
		t.Errorf("the binding names the database %q, want the resource's own name", got)
	}
	if got := binding.Properties[provider.PropertyPort]; got != "5432" {
		t.Errorf("the binding names port %q, want the one postgres listens on inside its network", got)
	}

	joined := strings.Join(machine.commands(), "\n")
	run := machine.at("'docker' 'run'")
	if run < 0 {
		t.Fatalf("nothing was stood up:\n%s", joined)
	}
	stood := machine.commands()[run]
	for _, want := range []string{
		"'--network' 'ocel-production-shop'",
		"ocel.class=production",
		"ocel.project=shop",
		"ocel.resource=main",
		"'--cap-drop' 'ALL'",
		"/var/lib/postgresql/data",
		"@sha256:",
		binding.Properties[provider.PropertyHost],
	} {
		if !strings.Contains(stood, want) {
			t.Errorf("the container was stood up without %q:\n%s", want, stood)
		}
	}
	for _, published := range []string{"--publish", "'-p'"} {
		if strings.Contains(stood, published) {
			t.Errorf("the container publishes a port on the box, and a database only its project's network reaches is the whole boundary:\n%s", stood)
		}
	}
}

func TestAPostgresPasswordNeverRidesACommandLine(t *testing.T) {
	t.Parallel()

	machine := &box{}
	binding, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil)
	if err != nil {
		t.Fatalf("Postgres() = %v", err)
	}
	password := binding.Properties[provider.PropertyPassword]
	if len(password) < 32 {
		t.Fatalf("the password is %d characters, and it is the one thing between the project network and the data", len(password))
	}
	for _, command := range machine.commands() {
		if strings.Contains(command, password) {
			t.Fatalf("the password rode a command line, where the box's process table and every refusal quote it:\n%s", command)
		}
	}
	if !slices.ContainsFunc(machine.carried(), func(fed string) bool { return strings.Contains(fed, password) }) {
		t.Error("the password reached the box on no stdin, so the container was handed none")
	}
}

func TestWhatABoxKeepsOfAPostgresPasswordIsSealedToThatResource(t *testing.T) {
	t.Parallel()

	machine := &box{}
	binding, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil)
	if err != nil {
		t.Fatalf("Postgres() = %v", err)
	}
	password := binding.Properties[provider.PropertyPassword]
	keeping := machine.at("/kept/")
	if keeping < 0 {
		t.Fatalf("nothing was kept on the box, so the next deploy mints a password the standing server was never handed:\n%s",
			strings.Join(machine.commands(), "\n"))
	}
	for at, command := range machine.commands() {
		if !strings.Contains(command, "/kept/") {
			continue
		}
		fed := machine.carried()[at]
		raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(fed))
		if strings.Contains(fed, password) || strings.Contains(string(raw), password) {
			t.Errorf("the box keeps the password in plaintext under %s, where it outlives every deploy", host.KeptPath(edge.ClassProduction, "prod-web-r0a1b2c3d-main-pg"))
		}
	}
	sealing := machine.at(host.SealHelper)
	if sealing < 0 {
		t.Fatal("no value was sealed")
	}
	for _, want := range []string{"'--project' 'shop'", "'--binding' 'main'"} {
		if !strings.Contains(machine.commands()[sealing], want) {
			t.Errorf("the password is sealed by %q and is not bound to %s, so a sealed value moved under another resource still opens", machine.commands()[sealing], want)
		}
	}
}

func TestAPostgresVersionNoImageIsPinnedForIsRefusedBeforeAnythingStands(t *testing.T) {
	t.Parallel()

	machine := &box{}
	_, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "9"), nil)
	if err == nil {
		t.Fatal("a postgres 9 stood up, and nothing pins an image for it")
	}
	for _, want := range []string{"main", `"9"`, "14, 15, 16, 17"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads %q and never says %s", err, want)
		}
	}
	if len(machine.commands()) != 0 {
		t.Errorf("the box was reached before the refusal: %v", machine.commands())
	}
}

func TestASecondDeployBindsToThePasswordTheStandingPostgresWasHanded(t *testing.T) {
	t.Parallel()

	machine := &box{}
	holdingAPostgres(machine)
	binding, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil)
	if err != nil {
		t.Fatalf("Postgres() = %v", err)
	}
	if got := binding.Properties[provider.PropertyPassword]; got != standingPassword {
		t.Errorf("the binding carries a password the standing server was never handed, and every app bound to it is locked out")
	}
}

func TestADeployThatLosesTheNameToAnotherSharesTheWinnersPostgres(t *testing.T) {
	t.Parallel()

	machine := &box{}
	holdingAPostgres(machine)
	machine.refuses = func(command string) (session.Result, bool) {
		if strings.Contains(command, "'docker' 'run'") {
			return session.Result{Code: 125, Stderr: `docker: Error response from daemon: Conflict. The container name "/prod-web-r0a1b2c3d-main-pg" is already in use by container "9f2c"`}, true
		}
		return session.Result{}, false
	}
	binding, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil)
	if err != nil {
		t.Fatalf("Postgres() = %v, and two deploys standing one postgres up at once end up sharing it", err)
	}
	if got := binding.Properties[provider.PropertyPassword]; got != standingPassword {
		t.Error("the deploy that lost the name bound to a password the server that won was never handed")
	}
	if strings.Contains(strings.Join(machine.commands(), "\n"), "docker rm") {
		t.Error("the deploy that lost the name removed a container, and the only one standing is the winner's")
	}
}

func TestARemovedPostgresTakesItsVolumeWithIt(t *testing.T) {
	t.Parallel()

	machine := &box{}
	p := over(machine)
	in := aPostgres(t, "17")
	binding, err := p.ProvisionPostgres(context.Background(), in, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := binding.Properties[provider.PropertyHost]
	before := len(machine.commands())
	if err := p.RemoveResource(context.Background(), in.Ref, binding, nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	joined := strings.Join(machine.commands()[before:], "\n")
	if !strings.Contains(joined, "docker rm") || !strings.Contains(joined, name) {
		t.Errorf("a teardown ran %q and left the server standing", joined)
	}
	if !strings.Contains(joined, "docker volume rm") {
		t.Errorf("a teardown ran %q and left the data on the box, where no later deploy reclaims it", joined)
	}
	if !strings.Contains(joined, host.BackupsDir(edge.ClassProduction, name)) {
		t.Errorf("a teardown ran %q and left the dumps taken of it on the box", joined)
	}
	if !strings.Contains(joined, name+"-retired") {
		t.Errorf("a teardown ran %q and would leave the server an interrupted upgrade retired", joined)
	}
	if !strings.Contains(joined, host.KeptPath(edge.ClassProduction, name)) {
		t.Errorf("a teardown ran %q and left the sealed password behind", joined)
	}
}

func TestARemovalReachesOnlyTheServerItsOwnStackStoodUp(t *testing.T) {
	t.Parallel()

	machine := &box{}
	in := aPostgres(t, "17")
	foreign := provider.Binding{
		Type: provider.BindingPostgres, Name: "main",
		Properties: map[string]string{provider.PropertyHost: "someone-elses-container"},
	}
	if err := over(machine).RemoveResource(context.Background(), in.Ref, foreign, nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	joined := strings.Join(machine.commands(), "\n")
	if strings.Contains(joined, "someone-elses-container") {
		t.Errorf("a removal took its target from the record it was handed and ran %q: the name is the stack's and the resource's, and a record naming another container removes that one", joined)
	}
	if !strings.Contains(joined, "prod-web-r0a1b2c3d-main-pg") {
		t.Errorf("a removal ran %q and never reached the server this stack stood up for main", joined)
	}
}

const takenDump = "/var/lib/ocel/production/backups/prod-web-r0a1b2c3d-main-pg/20260919T000000Z.dump"

func heldByAnotherMajor(machine *box, state string) {
	holdingAPostgres(machine)
	machine.refuses = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "docker volume ls") && strings.Contains(command, host.LabelGeneration):
			return session.Result{Stdout: "16\n"}, true
		case strings.Contains(command, ".State.Status"):
			return session.Result{Stdout: state + " postgres:16 0123456789ab\n"}, true
		case strings.Contains(command, host.BackupsHelper) && strings.Contains(command, "'dump'"):
			return session.Result{Stdout: takenDump + "\n"}, true
		}
		return session.Result{}, false
	}
}

func TestAPostgresDeclaredUnderANewerMajorIsDumpedThenSwappedWithAWayBack(t *testing.T) {
	t.Parallel()

	machine := &box{}
	heldByAnotherMajor(machine, "running")
	binding, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil)
	if err != nil {
		t.Fatalf("Postgres() = %v", err)
	}
	if got := binding.Properties[provider.PropertyPassword]; got != standingPassword {
		t.Error("the upgraded server is bound under another password, and every app bound to the old one is locked out")
	}
	dumped := machine.at("'dump'")
	swapped := machine.at("docker rename")
	if dumped < 0 || swapped < 0 || dumped > swapped {
		t.Fatalf("the dump ran at %d and the swap at %d: the old server is dumped while it still runs, before anything about it changes:\n%s",
			dumped, swapped, strings.Join(machine.commands(), "\n"))
	}
	swap := machine.commands()[swapped]
	for _, want := range []string{
		"trap ", "docker stop 'prod-web-r0a1b2c3d-main-pg'",
		"'prod-web-r0a1b2c3d-main-pg-retired'",
		"src=prod-web-r0a1b2c3d-main-pg-g17",
		"'restore' 'prod-web-r0a1b2c3d-main-pg' 'main' '" + takenDump + "'",
		"docker volume rm 'prod-web-r0a1b2c3d-main-pg-g16'",
		"docker start 'prod-web-r0a1b2c3d-main-pg'",
	} {
		if !strings.Contains(swap, want) {
			t.Errorf("the swap never runs %s:\n%s", want, swap)
		}
	}
	if restored, removed := strings.Index(swap, "'restore'"), strings.LastIndex(swap, "docker volume rm 'prod-web-r0a1b2c3d-main-pg-g16'"); removed < restored {
		t.Errorf("the old data is removed before the new server has it back:\n%s", swap)
	}
	for at, command := range machine.commands() {
		if at != swapped && (strings.Contains(command, "docker rm") || strings.Contains(command, "docker stop")) {
			t.Errorf("the old server was touched outside the one script that can put it back: %s", command)
		}
	}
}

func TestAStoppedPostgresUnderAnotherMajorIsRefusedBecauseNothingCanBeDumpedFromIt(t *testing.T) {
	t.Parallel()

	machine := &box{}
	heldByAnotherMajor(machine, "exited")
	_, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil)
	if err == nil {
		t.Fatal("a server that is not running was upgraded, and there was nothing to dump its data from")
	}
	for _, want := range []string{"main", "16", "17"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads %q and never says %s", err, want)
		}
	}
	joined := strings.Join(machine.commands(), "\n")
	if strings.Contains(joined, "docker rm") || strings.Contains(joined, "'docker' 'run'") || strings.Contains(joined, "docker rename") {
		t.Errorf("the stopped server was touched before the refusal:\n%s", joined)
	}
}

func TestAVolumeIsLabelledWithTheMajorThatInitialisedIt(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if _, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil); err != nil {
		t.Fatal(err)
	}
	kept := machine.commands()[machine.at("'docker' 'volume' 'create'")]
	if !strings.Contains(kept, "'"+host.LabelGeneration+"=17'") {
		t.Errorf("the volume was created as %q, and the next deploy cannot tell which major wrote what is on it", kept)
	}
}

func TestAPostgresDeclaringNoVersionRunsTheOneEverySdkDeclaresByDefault(t *testing.T) {
	t.Parallel()

	machine := &box{}
	in := aPostgres(t, "")
	in.Resource.Postgres = nil
	if _, err := over(machine).ProvisionPostgres(context.Background(), in, nil); err != nil {
		t.Fatalf("Postgres() of a resource naming no version = %v, and a version is a preference, not something a deploy is refused for leaving out", err)
	}
	stood := machine.commands()[machine.at("'docker' 'run'")]
	if !strings.Contains(stood, "postgres:17.") {
		t.Errorf("a postgres naming no version was stood up as:\n%s\nwant the major every sdk declares when the app names none", stood)
	}
}

func TestALoginThatDoesNotOwnTheStateDirectoryKeepsThePasswordThroughSudo(t *testing.T) {
	t.Parallel()

	machine := &box{unsocket: true}
	machine.refuses = func(command string) (session.Result, bool) {
		owns := strings.Contains(command, "/kept/") || strings.Contains(command, ".env")
		if owns && !strings.HasPrefix(command, "sudo ") {
			return session.Result{Code: 1, Stderr: "mkdir: cannot create directory '/var/lib/ocel': Permission denied"}, true
		}
		return session.Result{}, false
	}
	binding, err := over(machine).ProvisionPostgres(context.Background(), aPostgres(t, "17"), nil)
	if err != nil {
		t.Fatalf("Postgres() as a login with sudo that does not own the state directory = %v, and a bootstrap login deploys as readily as the deploy login does", err)
	}
	if binding.Properties[provider.PropertyPassword] == "" {
		t.Error("the binding carries no password")
	}
	kept := machine.commands()[len(machine.commands())-1]
	for _, command := range machine.commands() {
		if strings.HasPrefix(command, "sudo ") && strings.Contains(command, "/kept/") && strings.Contains(command, "ln ") {
			kept = command
		}
	}
	if !strings.Contains(kept, "chown") {
		t.Errorf("what root kept is left root's, and the deploy login that owns the state directory can no longer open it:\n%s", kept)
	}
}
