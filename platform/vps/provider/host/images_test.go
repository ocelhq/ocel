package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	imageRef = "ocel/shop/web:sha256-abc"
	imageID  = "sha256:abcdef"
)

func (b *bench) feeds() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.fed...)
}

func imaged(b *bench, present bool) {
	b.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "docker image ls"):
			if present {
				return session.Result{Stdout: imageID + "\n"}, true
			}
			return session.Result{}, true
		case strings.Contains(command, "docker load"):
			present = true
			return session.Result{Stdout: "Loaded image: " + imageRef + "\n"}, true
		default:
			return session.Result{}, false
		}
	}
}

func TestAnImageTheMachineAlreadyHasIsAnswered(t *testing.T) {
	rig := machine(nil)
	imaged(rig, true)

	has, err := rig.host().HasImage(context.Background(), imageRef)
	if err != nil {
		t.Fatalf("HasImage() = %v", err)
	}
	if !has {
		t.Error("HasImage() says no over a machine whose daemon names the image ref, so an unchanged redeploy would stream the whole image again")
	}
	for _, command := range rig.commands() {
		if strings.Contains(command, quoted(imageRef)) {
			return
		}
	}
	t.Errorf("no command named %s, so the answer is about something else: %v", imageRef, rig.commands())
}

func TestAnImageTheMachineDoesNotHaveIsAbsentRatherThanAFailure(t *testing.T) {
	rig := machine(nil)
	imaged(rig, false)

	has, err := rig.host().HasImage(context.Background(), imageRef)
	if err != nil {
		t.Fatalf("HasImage() over a machine that does not have it = %v, want an absence", err)
	}
	if has {
		t.Error("HasImage() says yes over a machine whose daemon names nothing")
	}
}

func TestADaemonThatDoesNotAnswerIsRefusedRatherThanReadAsAnAbsence(t *testing.T) {
	rig := machine(nil)
	rig.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker image ls") {
			return session.Result{Code: 1, Stderr: "Cannot connect to the Docker daemon"}, true
		}
		return session.Result{}, false
	}

	_, err := rig.host().HasImage(context.Background(), imageRef)
	if err == nil {
		t.Fatal("HasImage() read a daemon that is not running as an image that is merely absent, so the transfer would be attempted against nothing")
	}
	if !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Errorf("HasImage() = %v, want the machine's own reason", err)
	}
}

func TestTheImageIsFedToTheDaemonAndTheCoordinateIsCheckedAfterwards(t *testing.T) {
	rig := machine(nil)
	imaged(rig, false)

	said, err := rig.host().LoadImage(context.Background(), imageRef, strings.NewReader("tar-bytes"))
	if err != nil {
		t.Fatalf("LoadImage() = %v", err)
	}
	if !strings.Contains(said, imageRef) {
		t.Errorf("LoadImage() said %q, want what the daemon said it loaded", said)
	}
	if !strings.Contains(strings.Join(rig.feeds(), "\n"), "tar-bytes") {
		t.Errorf("the tar never reached the machine: %v", rig.feeds())
	}

	var loaded, checked bool
	for _, command := range rig.commands() {
		switch {
		case strings.Contains(command, "docker load"):
			loaded = true
			if !strings.Contains(command, "flock -x "+quoted(imagesLock)+" docker load") {
				t.Errorf("the load runs as %q and takes no lock: two deploys loading onto one box at once share base layers, and the daemon's import writes each blob under one ingest ref, so the second load finds the first's lock and ends with an image missing content", command)
			}
		case loaded && strings.Contains(command, "docker image ls"):
			checked = true
		}
	}
	if !loaded || !checked {
		t.Errorf("a load that is never checked promotes an image ref the machine may not answer to: %v", rig.commands())
	}
}

func TestALoadThatLeavesTheCoordinateUnansweredIsRefused(t *testing.T) {
	rig := machine(nil)
	rig.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "docker image ls"):
			return session.Result{}, true
		case strings.Contains(command, "docker load"):
			return session.Result{Stdout: "Loaded image: something-else\n"}, true
		case strings.Contains(command, "docker events"):
			return session.Result{Stdout: "containerd[1]: content digest sha256:abc: not found\n--- disk\n/dev/root 20G 19G 1G 95% /\n"}, true
		default:
			return session.Result{}, false
		}
	}

	_, err := rig.host().LoadImage(context.Background(), imageRef, strings.NewReader("tar-bytes"))
	if err == nil {
		t.Fatal("LoadImage() succeeded where the machine answers to no such imageRef afterwards")
	}
	for _, named := range []string{imageRef, "content digest sha256:abc", "95%"} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("LoadImage() = %v, want %q in it: a load the box took and then does not have explains itself with what the box's engine and disk say, or the cause is read off the wrong machine", err, named)
		}
	}
}

func refusedPull(said string) (*bench, *int) {
	rig := machine(nil)
	var asked int
	rig.answer = func(command string) (session.Result, bool) {
		if !strings.Contains(command, "docker pull") {
			return session.Result{}, false
		}
		asked++
		return session.Result{Code: 1, Stderr: said}, true
	}
	return rig, &asked
}

const (
	plainHex  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	markedHex = "4295030000000000000000000000000000000000000000000000000000000000"
)

func pulled(t *testing.T, rig *bench, server, hex string) error {
	t.Helper()
	_, err := rig.host().PullImage(context.Background(),
		images.Registry{Server: server, Namespace: "acme"},
		server+"/acme/web:sha256-"+hex, "sha256:"+hex)
	return err
}

func TestAFatalPullIsNotAskedAgainBecauseTheRegistryAnswersOnPortFiveThousand(t *testing.T) {
	rig, asked := refusedPull("Error response from daemon: manifest unknown")

	if err := pulled(t, rig, "registry.example.com:5000", plainHex); err == nil {
		t.Fatal("PullImage() = nil over a machine whose daemon says the manifest is unknown")
	}
	if *asked != 1 {
		t.Errorf("the machine was told to pull %d times over a manifest that does not exist, want the refusal taken at its word: "+
			"the registry's own port is read as a 500 and every fatal pull is waited out five times", *asked)
	}
}

func TestAFatalPullIsNotAskedAgainBecauseTheDigestHexReadsAsAStatusCode(t *testing.T) {
	rig, asked := refusedPull("Error response from daemon: manifest unknown")

	if err := pulled(t, rig, "registry.example.com:9443", markedHex); err == nil {
		t.Fatal("PullImage() = nil over a machine whose daemon says the manifest is unknown")
	}
	if *asked != 1 {
		t.Errorf("the machine was told to pull %d times over a manifest that does not exist, want the refusal taken at its word: "+
			"the digest ocel pinned contains the hex 429 and 503, and a digest is random", *asked)
	}
}

func TestAThrottledPullIsStillAskedAgainWhereTheDaemonSaysSo(t *testing.T) {
	rig, asked := refusedPull("toomanyrequests: You have reached your pull rate limit")

	if err := pulled(t, rig, "registry.example.com:9443", plainHex); err == nil {
		t.Fatal("PullImage() = nil over a registry that throttled every pull")
	}
	if *asked != pullAttempts {
		t.Errorf("the machine was told to pull %d times over a registry that throttled it, want %d", *asked, pullAttempts)
	}
}

func TestALoginOutsideTheDockerGroupReachesTheDaemonAsRoot(t *testing.T) {
	rig := machine(nil)
	rig.facts = session.Facts{Systemd: true}
	rig.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker") && !strings.HasPrefix(command, "sudo -n ") {
			return session.Result{Code: 1, Stderr: "permission denied while trying to connect to the Docker daemon socket"}, true
		}
		if strings.Contains(command, "docker image ls") {
			return session.Result{Stdout: imageID + "\n"}, true
		}
		return session.Result{}, false
	}

	has, err := rig.host().HasImage(context.Background(), imageRef)
	if err != nil {
		t.Fatalf("HasImage() as a login outside the docker group = %v", err)
	}
	if !has {
		t.Error("HasImage() gave up on a login that cannot reach the socket unelevated rather than becoming root")
	}
}

func TestADaemonThatIsDownIsNotReadAsALoginOutsideTheDockerGroup(t *testing.T) {
	rig := machine(nil)
	rig.facts = session.Facts{Systemd: true}
	var probes int
	rig.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, dockerReach) {
			probes++
			return session.Result{Code: 1, Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}, true
		}
		if strings.HasPrefix(command, "sudo -n ") {
			return session.Result{Code: 1, Stderr: "sudo: a password is required"}, true
		}
		return session.Result{}, false
	}
	h := rig.host()

	_, err := h.HasImage(context.Background(), imageRef)
	if err == nil {
		t.Fatal("HasImage() over a machine whose daemon is down succeeded")
	}
	if !strings.Contains(err.Error(), "Is the docker daemon running?") {
		t.Errorf("HasImage() = %v, want the reason the unelevated probe gave", err)
	}
	if strings.Contains(err.Error(), "password") {
		t.Errorf("HasImage() = %v: a daemon that is down is reported as a login that cannot become root", err)
	}

	if _, err := h.HasImage(context.Background(), imageRef); err == nil {
		t.Fatal("HasImage() = nil on a second ask over the same dead daemon")
	}
	if probes != 2 {
		t.Errorf("the daemon was probed %d times over two asks, want one probe each: a refusal that is cached leaves the deploy unable to recover once the daemon returns", probes)
	}
}

func TestTheDaemonIsFoundOnceHoweverManyImagesAreAskedAbout(t *testing.T) {
	rig := machine(nil)
	imaged(rig, true)
	h := rig.host()

	for range 3 {
		if _, err := h.HasImage(context.Background(), imageRef); err != nil {
			t.Fatal(err)
		}
	}
	var found int
	for _, command := range rig.commands() {
		if strings.Contains(command, dockerReach) {
			found++
		}
	}
	if found > 1 {
		t.Errorf("the daemon was looked for %d times over three questions about images: %v", found, rig.commands())
	}
}
