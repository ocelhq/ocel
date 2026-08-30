package vps_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func helperCalls(machine *box, verb string) []string {
	var called []string
	for _, command := range machine.commands() {
		if strings.Contains(command, "/usr/local/lib/ocel/releases") && strings.Contains(command, "'"+verb+"'") {
			called = append(called, command)
		}
	}
	return called
}

func TestAStoodUpReleaseIsRecordedAtTheHeadOfItsWindow(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if _, err := over(machine).ProvisionContainers(context.Background(), aStack(t, anApp()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	called := helperCalls(machine, "promote")
	if len(called) != 1 {
		t.Fatalf("standing the release up ran %d promotes, want the one that names what the box most recently served", len(called))
	}
	for _, want := range []string{"'web'", "'production'", "'" + loadedCoordinate + "'"} {
		if !strings.Contains(called[0], want) {
			t.Errorf("the promote ran as %q and never names %s", called[0], want)
		}
	}
	joined := strings.Join(machine.commands(), "\n")
	if stood := strings.Index(joined, "'--detach'"); stood < 0 || stood > strings.Index(joined, "'promote'") {
		t.Error("the window head was written before the container stood up, and the head is the ref the box is actually serving")
	}
}

func TestAReleaseThatNeverStoodUpRecordsNothing(t *testing.T) {
	t.Parallel()

	machine := &box{refuses: func(command string) (session.Result, bool) {
		if !strings.Contains(command, "'--detach'") {
			return session.Result{}, false
		}
		return session.Result{Code: 1, Stderr: "refused"}, true
	}}
	if _, err := over(machine).ProvisionContainers(context.Background(), aStack(t, anApp()), nil); err == nil {
		t.Fatal("ProvisionContainers() succeeded over a box that never ran the container")
	}
	if called := helperCalls(machine, "promote"); len(called) != 0 {
		t.Errorf("a release that never stood up recorded %v, and a failed release is never what the box most recently served", called)
	}
}

func TestTheWindowIsWrittenUnderNoElevationAtAll(t *testing.T) {
	t.Parallel()

	for name, run := range map[string]func(*vps.Provider, providerkit.StackRef) error{
		"promote": func(p *vps.Provider, ref providerkit.StackRef) error {
			_, err := p.ProvisionContainers(context.Background(), aStack(t, anApp()), nil)
			return err
		},
		"forget": func(p *vps.Provider, ref providerkit.StackRef) error {
			return p.ForgetReleases(context.Background(), ref, "web", nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			machine := &box{unsocket: true}
			ref := aStack(t, anApp()).Ref
			if err := run(over(machine), ref); err != nil {
				t.Fatalf("%s over a login outside the docker group = %v", name, err)
			}
			called := helperCalls(machine, name)
			if len(called) != 1 {
				t.Fatalf("the %s ran %d times, want one", name, len(called))
			}
			if strings.Contains(called[0], "sudo") {
				t.Errorf("the %s ran as %q: it touches no daemon, and a window root writes is a window the deploy login can no longer rewrite", name, called[0])
			}
		})
	}
}

func TestATeardownDropsTheWindowBeforeAnythingSweepsAgainstIt(t *testing.T) {
	t.Parallel()

	machine := &box{}
	ref := aStack(t, anApp()).Ref
	if err := over(machine).ForgetReleases(context.Background(), ref, "web", nil); err != nil {
		t.Fatalf("ForgetReleases() = %v", err)
	}
	called := helperCalls(machine, "forget")
	if len(called) != 1 {
		t.Fatalf("the teardown ran %d forgets, want the one that drops the window the torn-down stack wrote", len(called))
	}
	for _, want := range []string{"'web'", "'production'"} {
		if !strings.Contains(called[0], want) {
			t.Errorf("the forget ran as %q and never names %s", called[0], want)
		}
	}
}

func TestASweepListsOneRepositoryAndNeverForces(t *testing.T) {
	t.Parallel()

	machine := &box{}
	ref := aStack(t, anApp()).Ref
	if err := over(machine).ReconcileImages(context.Background(), ref, "web", loadedCoordinate, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}
	called := helperCalls(machine, "reconcile")
	if len(called) != 1 {
		t.Fatalf("the sweep ran %d reconciles, want one", len(called))
	}
	repository, _ := host.Repository(loadedCoordinate)
	if !strings.Contains(called[0], "'"+repository+"'") {
		t.Errorf("the reconcile ran as %q, want it scoped to %q: the filter and the desired set are computed over one scope", called[0], repository)
	}
	joined := strings.Join(machine.commands(), "\n")
	for _, never := range []string{"rmi -f", "image rm -f", "image prune", "--force"} {
		if strings.Contains(joined, never) {
			t.Errorf("the sweep ran %q: the box is the customer's and may hold images ocel did not put there", never)
		}
	}
}

func TestAnAppNameOfMetacharactersReachesTheHelperAsOneWord(t *testing.T) {
	t.Parallel()

	for _, app := range []string{"web; rm -rf /", "$(id)", "'; docker rmi $(docker images -q); #"} {
		machine := &box{}
		ref := aStack(t, anApp()).Ref
		_ = over(machine).ReconcileImages(context.Background(), ref, app, loadedCoordinate, nil)
		for _, command := range machine.commands() {
			if !strings.Contains(command, app) {
				continue
			}
			if !strings.Contains(command, quoted(app)) {
				t.Errorf("the name %q reached the wire as %q outside a quoted word", app, command)
			}
		}
	}
}

func TestACoordinateNamingNoRepositoryIsRefusedRatherThanSwept(t *testing.T) {
	t.Parallel()

	machine := &box{}
	ref := aStack(t, anApp()).Ref
	err := over(machine).ReconcileImages(context.Background(), ref, "web", "ocel/web", nil)
	if err == nil {
		t.Fatal("a coordinate carrying no tag swept anyway, and a filter that names a whole repository removes the wrong thing")
	}
	if len(helperCalls(machine, "reconcile")) != 0 {
		t.Error("the sweep ran before the coordinate was read")
	}
}

func TestARollbackToARetainedDigestTransfersNothingAndTakesTheWindowHead(t *testing.T) {
	t.Parallel()

	machine := &box{holds: true}
	ref := aStack(t, anApp()).Ref
	if err := over(machine).EnsureRelease(context.Background(), ref, "web", "prod-web-x", loadedCoordinate, nil); err != nil {
		t.Fatalf("EnsureRelease() = %v", err)
	}
	joined := strings.Join(machine.commands(), "\n")
	for _, never := range []string{"docker load", "docker pull", "docker build"} {
		if strings.Contains(joined, never) {
			t.Errorf("re-running a retained digest ran %q, and the box already holds it", never)
		}
	}
	called := helperCalls(machine, "promote")
	if len(called) != 1 || !strings.Contains(called[0], "'"+loadedCoordinate+"'") {
		t.Fatalf("the rollback recorded %v, want the retained ref moved to the head of the window", called)
	}
	if !strings.Contains(joined, "'--detach'") {
		t.Error("the rollback stood nothing up at all")
	}
}

func TestARollbackRevivesTheContainerTheReleaseRecorded(t *testing.T) {
	t.Parallel()

	machine := &box{holds: true}
	ref := aStack(t, anApp()).Ref
	stood := host.ContainerName(ref.Name.String(), "web", deployment, loadedCoordinate)
	if err := over(machine).EnsureRelease(context.Background(), ref, "web", stood, loadedCoordinate, nil); err != nil {
		t.Fatalf("EnsureRelease() = %v", err)
	}
	joined := strings.Join(machine.commands(), "\n")
	if !strings.Contains(joined, quoted(stood)) {
		t.Errorf("the rollback never named %s, the container the release wrote down", stood)
	}
	renamed := host.ContainerName(ref.Name.String(), "web", "", loadedCoordinate)
	if renamed == stood {
		t.Fatal("the coordinate and the deployment name the same container, and this test proves nothing")
	}
	if strings.Contains(joined, quoted(renamed)) {
		t.Errorf("the rollback named %s, a container no release ever stood up: an ensure that renames stands a second container up beside the one already serving", renamed)
	}
}

func TestARollbackPastTheWindowSaysDeployAgain(t *testing.T) {
	t.Parallel()

	machine := &box{}
	ref := aStack(t, anApp()).Ref
	err := over(machine).EnsureRelease(context.Background(), ref, "web", "prod-web-x", loadedCoordinate, nil)
	if err == nil {
		t.Fatal("a rollback past the window succeeded over a box holding no such image")
	}
	if !strings.Contains(err.Error(), "deploy again") {
		t.Errorf("the refusal reads %q and never says what to do instead", err)
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeNotReady {
		t.Errorf("the refusal reads %#v, want the code %q", err, providerkit.CodeNotReady)
	}
	if len(helperCalls(machine, "promote")) != 0 {
		t.Error("a rollback that refused still moved a ref to the window head")
	}
}

func quoted(arg string) string { return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'" }
