package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
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

func TestAStartedReleaseIsRecordedAtTheHeadOfItsWindow(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if _, err := over(machine).ProvisionContainers(context.Background(), aStack(t, anApp()), nil); err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	called := helperCalls(machine, "promote")
	if len(called) != 1 {
		t.Fatalf("starting the release ran %d promotes, want the one that names what the box most recently served", len(called))
	}
	for _, want := range []string{"'shop/web'", "'production'", "'" + loadedImageRef + "'"} {
		if !strings.Contains(called[0], want) {
			t.Errorf("the promote ran as %q and never names %s", called[0], want)
		}
	}
	joined := strings.Join(machine.commands(), "\n")
	if detached := strings.Index(joined, "'--detach'"); detached < 0 || detached > strings.Index(joined, "'promote'") {
		t.Error("the window head was written before the container started, and the head is the ref the box is actually serving")
	}
}

func TestAReleaseThatNeverStartedRecordsNothing(t *testing.T) {
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
		t.Errorf("a release that never started recorded %v, and a failed release is never what the box most recently served", called)
	}
}

func TestTheWindowIsWrittenUnderNoElevationAtAll(t *testing.T) {
	t.Parallel()

	for name, run := range map[string]func(*vps.Provider, provider.StackRef) error{
		"promote": func(p *vps.Provider, ref provider.StackRef) error {
			_, err := p.ProvisionContainers(context.Background(), aStack(t, anApp()), nil)
			return err
		},
		"forget": func(p *vps.Provider, ref provider.StackRef) error {
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

func TestAReleaseThatNeverReachedItsRecordSweepsItsOwnImageAnyway(t *testing.T) {
	t.Parallel()

	machine := &box{refuses: func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "echo present"):
			return session.Result{Stdout: "present\n"}, true
		case strings.Contains(command, "/usr/local/lib/ocel/records"):
			return session.Result{Code: 1, Stderr: "refused"}, true
		}
		return session.Result{}, false
	}}
	p := over(machine)

	if _, err := p.Stacks().Provision(context.Background(), aStack(t, anApp()), nil); err == nil {
		t.Fatal("Provision() succeeded over a box whose record tier refused, and this test needs the failure path")
	}
	called := helperCalls(machine, "reconcile")
	if len(called) != 1 {
		t.Fatalf("the failed release ran %d reconciles, want the one that sweeps the image it left: no timer, no cron and no unit sweeps this box between deploys", len(called))
	}
	repository, _ := host.Repository(loadedImageRef)
	if !strings.Contains(called[0], "'"+repository+"'") {
		t.Errorf("the sweep ran as %q and never names %q, the repository the release it could not finish left an image under", called[0], repository)
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
	for _, want := range []string{"'shop/web'", "'production'"} {
		if !strings.Contains(called[0], want) {
			t.Errorf("the forget ran as %q and never names %s", called[0], want)
		}
	}
}

func TestASweepListsOneRepositoryAndNeverForces(t *testing.T) {
	t.Parallel()

	machine := &box{}
	ref := aStack(t, anApp()).Ref
	if err := over(machine).ReconcileImages(context.Background(), ref, "web", loadedImageRef, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}
	called := helperCalls(machine, "reconcile")
	if len(called) != 1 {
		t.Fatalf("the sweep ran %d reconciles, want one", len(called))
	}
	repository, _ := host.Repository(loadedImageRef)
	if !strings.Contains(called[0], "'"+repository+"'") {
		t.Errorf("the reconcile ran as %q, want it scoped to %q: the filter and the desired set are computed over one scope", called[0], repository)
	}
	joined := strings.Join(machine.commands(), "\n")
	for _, never := range []string{"rmi -f", "image rm -f", "image prune", "--force"} {
		if strings.Contains(joined, never) {
			t.Errorf("the sweep ran %q: the box is the customer's and may have images ocel did not put there", never)
		}
	}
}

func TestAnAppNameOfMetacharactersReachesTheHelperAsOneWord(t *testing.T) {
	t.Parallel()

	for _, app := range []string{"web; rm -rf /", "$(id)", "'; docker rmi $(docker images -q); #"} {
		machine := &box{}
		ref := aStack(t, anApp()).Ref
		_ = over(machine).ReconcileImages(context.Background(), ref, app, loadedImageRef, nil)
		quotedCommands := 0
		for _, command := range machine.commands() {
			if !strings.Contains(command, app) {
				continue
			}
			if !strings.Contains(command, quoted("shop/"+app)) {
				t.Errorf("the name %q reached the wire as %q outside a quoted word", app, command)
				continue
			}
			quotedCommands++
		}
		if quotedCommands == 0 {
			t.Errorf("no command the sweep ran included %q at all, so this test read nothing: a helper invocation that drops or mangles the app name passes it green", app)
		}
	}
}

func TestACoordinateNamingNoRepositoryIsRefusedRatherThanSwept(t *testing.T) {
	t.Parallel()

	for _, imageRef := range []string{
		"ocel/shop/web",
		"ocel/shop/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"ocel/shop/web:",
		"registry.invalid:5000/ocel/shop/web",
	} {
		machine := &box{}
		ref := aStack(t, anApp()).Ref
		err := over(machine).ReconcileImages(context.Background(), ref, "web", imageRef, nil)
		if err == nil {
			t.Errorf("%s swept anyway, and a filter that names anything but one repository removes the wrong thing", imageRef)
		}
		if len(helperCalls(machine, "reconcile")) != 0 {
			t.Errorf("%s reached the sweep before it was read", imageRef)
		}
	}
}

func TestADigestCoordinateNamesNoRepositoryToSweep(t *testing.T) {
	t.Parallel()

	pinned := "ocel/shop/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if repository, named := host.Repository(pinned); named {
		t.Errorf("Repository(%s) = %q, and a digest is not a tag: everything left of the last colon is the repository plus half a digest algorithm, which lists nothing and names nothing the desired set contains", pinned, repository)
	}
}

func quoted(arg string) string { return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'" }
