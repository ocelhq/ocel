package vps_test

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
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
			return p.ForgetReleases(context.Background(), ref, "web", nil, nil)
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
		case strings.Contains(command, "/usr/local/lib/ocel/keyvalues"):
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
	if err := over(machine).ForgetReleases(context.Background(), ref, "web", nil, nil); err != nil {
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
	if err := over(machine).ReconcileImages(context.Background(), ref, "web", loadedImageRef, nil, nil); err != nil {
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
		_ = over(machine).ReconcileImages(context.Background(), ref, app, loadedImageRef, nil, nil)
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
		err := over(machine).ReconcileImages(context.Background(), ref, "web", imageRef, nil, nil)
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

const registryImageRef = "registry.example.com/acme/shop.web:sha256-new"

func sweepingOff(removed ...string) *box {
	return &box{refuses: func(command string) (session.Result, bool) {
		if !strings.Contains(command, "/usr/local/lib/ocel/releases") || !strings.Contains(command, "'reconcile'") {
			return session.Result{}, false
		}
		var said strings.Builder
		for _, image := range removed {
			said.WriteString("removed " + image + "\nunused " + image + "\n")
		}
		return session.Result{Stdout: said.String()}, true
	}}
}

func sweepingOffWhileADeployClaims(claimed string, removed ...string) *box {
	sweeping := sweepingOff(removed...)
	return &box{refuses: func(command string) (session.Result, bool) {
		if strings.Contains(command, "/usr/local/lib/ocel/releases") && strings.Contains(command, "'claimed'") {
			if strings.Contains(command, quoted(claimed)) {
				return session.Result{Stdout: claimed + "\n"}, true
			}
			return session.Result{}, true
		}
		return sweeping.refuses(command)
	}}
}

func TestASweepLeavesInTheRegistryARefADeployClaimedAfterTheBoxReportedIt(t *testing.T) {
	t.Parallel()

	promoted := "registry.example.com/acme/shop.web:sha256-promoted"
	going := "registry.example.com/acme/shop.web:sha256-old"
	machine := sweepingOffWhileADeployClaims(promoted, promoted, going)
	store := fake.NewImages()

	if err := over(machine).ReconcileImages(context.Background(), aStack(t, anApp()).Ref, "web", registryImageRef, store, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got, want := store.Removed(), []string{going}; !slices.Equal(got, want) {
		t.Errorf("the registry was asked to remove %v, want %v: a deploy promoted %s after the box reported it, and a fresh box pulls it from the registry", got, want, promoted)
	}
	if got, want := settledRefs(machine), []string{going}; !slices.Equal(got, want) {
		t.Errorf("the sweep settled %v, want %v: a ref claimed again is not a ref the registry removed", got, want)
	}
	if asked := helperCalls(machine, "claimed"); len(asked) != 3 {
		t.Errorf("the sweep asked the box %d times what is claimed, want once before each registry delete and once after the one it made", len(asked))
	}
}

func settledRefs(machine *box) []string {
	var settled []string
	for _, command := range helperCalls(machine, "settle") {
		_, refs, _ := strings.Cut(command, "'settle'")
		for _, ref := range strings.Fields(refs) {
			settled = append(settled, strings.Trim(ref, "'"))
		}
	}
	return settled
}

func TestASweepSettlesOnlyTheRefsTheRegistryRemoved(t *testing.T) {
	t.Parallel()

	machine := sweepingOff("registry.example.com/acme/shop.web:sha256-old")
	store := fake.NewImages()
	store.FailRemovals(errors.New("UNSUPPORTED: The operation is unsupported."))

	if err := over(machine).ReconcileImages(context.Background(), aStack(t, anApp()).Ref, "web", registryImageRef, store, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got := settledRefs(machine); len(got) != 0 {
		t.Errorf("the sweep settled %v, want nothing: the registry refused the delete, and a settled ref is never reported again", got)
	}
}

func TestASweepSettlesARefOnceTheRegistryRemovedIt(t *testing.T) {
	t.Parallel()

	machine := sweepingOff("registry.example.com/acme/shop.web:sha256-old")

	if err := over(machine).ReconcileImages(context.Background(), aStack(t, anApp()).Ref, "web", registryImageRef, fake.NewImages(), nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got, want := settledRefs(machine), []string{"registry.example.com/acme/shop.web:sha256-old"}; !slices.Equal(got, want) {
		t.Errorf("the sweep settled %v, want %v", got, want)
	}
}

func TestASweepWithNoRegistryToReachSettlesOnlyTheRefsNoRegistryHolds(t *testing.T) {
	t.Parallel()

	machine := sweepingOff("ocel/shop-web:1111", "registry.example.com/acme/shop.web:sha256-old")

	if err := over(machine).ReconcileImages(context.Background(), aStack(t, anApp()).Ref, "web", loadedImageRef, nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if got, want := settledRefs(machine), []string{"ocel/shop-web:1111"}; !slices.Equal(got, want) {
		t.Errorf("the sweep settled %v, want %v: a removal run without the registry password must leave the registry's copy for one that has it", got, want)
	}
}

func TestASweepRemovesFromTheRegistryEveryImageItDroppedFromTheBox(t *testing.T) {
	t.Parallel()

	dropped := []string{"registry.example.com/acme/shop.web:sha256-old", "registry.example.com/acme/shop.web:sha256-older"}
	machine := sweepingOff(dropped...)
	store := fake.NewImages()
	ref := aStack(t, anApp()).Ref

	if err := over(machine).ReconcileImages(context.Background(), ref, "web", registryImageRef, store, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}
	if got := store.Removed(); !slices.Equal(got, dropped) {
		t.Errorf("the registry was asked to remove %v, want what the box dropped %v: a tag the box no longer holds is a tag nothing will ever pull again", got, dropped)
	}
}

func TestASweepNamesEveryImageItDroppedFromTheBox(t *testing.T) {
	t.Parallel()

	machine := sweepingOff("ocel/shop-web:1111", "ocel/shop-web:2222")
	log := &fake.Log{}

	if err := over(machine).ReconcileImages(context.Background(), aStack(t, anApp()).Ref, "web", loadedImageRef, nil, log); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}
	joined := strings.Join(log.Lines(), "\n")
	for _, want := range []string{"Removed web's unused image ocel/shop-web:1111", "Removed web's unused image ocel/shop-web:2222"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the sweep said %q, want %q", joined, want)
		}
	}
}

func TestASweepRemovesNothingFromTheRegistryWhenTheBoxDroppedNothing(t *testing.T) {
	t.Parallel()

	machine := sweepingOff()
	store := fake.NewImages()
	ref := aStack(t, anApp()).Ref

	if err := over(machine).ReconcileImages(context.Background(), ref, "web", registryImageRef, store, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}
	if got := store.Removed(); len(got) != 0 {
		t.Errorf("the registry was asked to remove %v, want nothing: the window and the running containers still name every image it holds", got)
	}
}

func TestASweepOverNoRegistryDropsTheBoxImagesAndRemovesNothingElsewhere(t *testing.T) {
	t.Parallel()

	machine := sweepingOff(loadedImageRef)
	ref := aStack(t, anApp()).Ref

	if err := over(machine).ReconcileImages(context.Background(), ref, "web", loadedImageRef, nil, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}
	if len(helperCalls(machine, "reconcile")) != 1 {
		t.Error("the sweep never ran on the box")
	}
}

func TestASweepWhoseRegistryRefusesTheDeleteSucceedsAndNamesTheImageItLeft(t *testing.T) {
	t.Parallel()

	machine := sweepingOff("registry.example.com/acme/shop.web:sha256-old")
	store := fake.NewImages()
	store.FailRemovals(errors.New("UNSUPPORTED: The operation is unsupported."))
	log := &fake.Log{}
	ref := aStack(t, anApp()).Ref

	if err := over(machine).ReconcileImages(context.Background(), ref, "web", registryImageRef, store, log); err != nil {
		t.Fatalf("ReconcileImages() = %v, want nothing: the box was swept, and the registry refusing a delete is not the box failing", err)
	}
	joined := strings.Join(log.Lines(), "\n")
	for _, want := range []string{"WARN", "registry.example.com/acme/shop.web:sha256-old", "UNSUPPORTED"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the sweep said %q, want a warning that names %q", joined, want)
		}
	}
}

type racedSweep struct {
	machine *box
	store   provider.ImageStore
	tag     name.Tag
	push    provider.ImagePush
	claimed *atomic.Bool
}

func aSweepRacingADeploy(t *testing.T, claimsDuringTheDelete, boxHolds bool) racedSweep {
	t.Helper()
	claimed := &atomic.Bool{}
	inner := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		if r.Method == http.MethodDelete && claimsDuringTheDelete {
			claimed.Store(true)
		}
	}))
	t.Cleanup(served.Close)
	server := strings.TrimPrefix(served.URL, "http://")
	built := wrapped(t)
	ref := server + "/acme/shop.web:sha256-old"
	tag, err := name.NewTag(ref, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(tag, built); err != nil {
		t.Fatal(err)
	}
	machine := &box{hasImage: boxHolds}
	machine.refuses = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "/usr/local/lib/ocel/releases") && strings.Contains(command, "'reconcile'"):
			return session.Result{Stdout: "unused " + ref + "\n"}, true
		case strings.Contains(command, "/usr/local/lib/ocel/releases") && strings.Contains(command, "'claimed'"):
			if claimed.Load() {
				return session.Result{Stdout: ref + "\n"}, true
			}
			return session.Result{}, true
		case strings.Contains(command, "docker push"):
			if !boxHolds {
				return session.Result{Code: 1, Stderr: "An image does not exist locally with the tag: " + ref}, true
			}
			if err := remote.Write(tag, built); err != nil {
				return session.Result{Code: 1, Stderr: err.Error()}, true
			}
			return session.Result{}, true
		}
		return session.Result{}, false
	}
	store, err := over(machine).OpenRegistryImages(context.Background(), provider.RegistryTarget{Server: server, Username: "ada", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	return racedSweep{
		machine: machine,
		store:   store,
		tag:     tag,
		push:    provider.ImagePush{App: "web", Source: "ocel/shop/web@sha256:abc", ImageRef: ref, Built: built},
		claimed: claimed,
	}
}

func TestASweepPushesBackFromTheBoxARefADeployClaimedWhileTheRegistryDeletedIt(t *testing.T) {
	t.Parallel()

	raced := aSweepRacingADeploy(t, true, true)
	said := &fake.Log{}

	if err := over(raced.machine).ReconcileImages(context.Background(), aStack(t, anApp()).Ref, "web", registryImageRef, raced.store, said); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	if _, err := remote.Head(raced.tag); err != nil {
		t.Errorf("the registry does not hold %s after the sweep: %v; a deploy claimed it while the delete was in flight, and a fresh box pulls it from the registry", raced.tag, err)
	}
	if got := settledRefs(raced.machine); len(got) != 0 {
		t.Errorf("the sweep settled %v, want nothing: a ref the registry holds again is not a ref it removed", got)
	}
	var pushed string
	for _, command := range raced.machine.commands() {
		if strings.Contains(command, "docker push") {
			pushed = command
		}
	}
	if !strings.Contains(pushed, "docker login") || strings.Contains(pushed, "s3cret") {
		t.Errorf("the box pushed as %q, want it logged in to the registry with the password on stdin", pushed)
	}
	joined := strings.Join(said.Lines(), "\n")
	for _, want := range []string{"WARN", raced.push.ImageRef, "pushed it back"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the sweep said %q, want a warning that names %q", joined, want)
		}
	}
}

func TestASweepWarnsOfARefADeployClaimedWhileTheRegistryDeletedItThatTheBoxNoLongerHolds(t *testing.T) {
	t.Parallel()

	raced := aSweepRacingADeploy(t, true, false)
	said := &fake.Log{}

	if err := over(raced.machine).ReconcileImages(context.Background(), aStack(t, anApp()).Ref, "web", registryImageRef, raced.store, said); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}

	joined := strings.Join(said.Lines(), "\n")
	for _, want := range []string{"WARN", raced.push.ImageRef, "neither"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the sweep said %q, want a warning that names %q", joined, want)
		}
	}
	if got := settledRefs(raced.machine); len(got) != 0 {
		t.Errorf("the sweep settled %v, want nothing: a claimed ref is never one the sweep is done with", got)
	}
}

func TestADeployWhoseClaimLandsAfterTheSweepLooksAgainFindsTheRegistryLostItAndPushesIt(t *testing.T) {
	t.Parallel()

	raced := aSweepRacingADeploy(t, false, true)

	if err := over(raced.machine).ReconcileImages(context.Background(), aStack(t, anApp()).Ref, "web", registryImageRef, raced.store, nil); err != nil {
		t.Fatalf("ReconcileImages() = %v", err)
	}
	if got, want := settledRefs(raced.machine), []string{raced.push.ImageRef}; !slices.Equal(got, want) {
		t.Fatalf("the sweep settled %v, want %v: nothing claimed it when the sweep looked again", got, want)
	}
	raced.claimed.Store(true)

	held, err := raced.store.Has(context.Background(), raced.push)
	if err != nil {
		t.Fatalf("Has() = %v", err)
	}
	if held {
		t.Fatal("Has() = true after the sweep deleted the registry tag, so the deploy's check after provisioning would push nothing")
	}
	if err := (provider.ImagePushes{Store: raced.store, Pushes: []provider.ImagePush{raced.push}}).PushMissing(context.Background(), nil); err != nil {
		t.Fatalf("PushMissing() = %v", err)
	}
	if _, err := remote.Head(raced.tag); err != nil {
		t.Errorf("the registry does not hold %s after the deploy's check pushed it: %v", raced.tag, err)
	}
}
