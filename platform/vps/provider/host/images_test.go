package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	coordinate = "ocel/shop/web:sha256-abc"
	imageID    = "sha256:abcdef"
)

func (b *bench) carried() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.fed...)
}

func imaged(b *bench, holds bool) {
	b.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "docker image ls"):
			if holds {
				return session.Result{Stdout: imageID + "\n"}, true
			}
			return session.Result{}, true
		case strings.Contains(command, "docker load"):
			holds = true
			return session.Result{Stdout: "Loaded image: " + coordinate + "\n"}, true
		default:
			return session.Result{}, false
		}
	}
}

func TestAnImageTheMachineAlreadyHoldsIsAnswered(t *testing.T) {
	stood := machine(nil)
	imaged(stood, true)

	held, err := stood.host().HoldsImage(context.Background(), coordinate)
	if err != nil {
		t.Fatalf("HoldsImage() = %v", err)
	}
	if !held {
		t.Error("HoldsImage() says no over a machine whose daemon names the coordinate, so an unchanged redeploy would stream the whole image again")
	}
	for _, command := range stood.commands() {
		if strings.Contains(command, quoted(coordinate)) {
			return
		}
	}
	t.Errorf("no command named %s, so the answer is about something else: %v", coordinate, stood.commands())
}

func TestAnImageTheMachineDoesNotHoldIsAbsentRatherThanAFailure(t *testing.T) {
	stood := machine(nil)
	imaged(stood, false)

	held, err := stood.host().HoldsImage(context.Background(), coordinate)
	if err != nil {
		t.Fatalf("HoldsImage() over a machine that does not hold it = %v, want an absence", err)
	}
	if held {
		t.Error("HoldsImage() says yes over a machine whose daemon names nothing")
	}
}

func TestADaemonThatDoesNotAnswerIsRefusedRatherThanReadAsAnAbsence(t *testing.T) {
	stood := machine(nil)
	stood.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker image ls") {
			return session.Result{Code: 1, Stderr: "Cannot connect to the Docker daemon"}, true
		}
		return session.Result{}, false
	}

	_, err := stood.host().HoldsImage(context.Background(), coordinate)
	if err == nil {
		t.Fatal("HoldsImage() read a daemon that is not running as an image that is merely absent, so the transfer would be attempted against nothing")
	}
	if !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Errorf("HoldsImage() = %v, want the machine's own reason", err)
	}
}

func TestTheImageIsFedToTheDaemonAndTheCoordinateIsCheckedAfterwards(t *testing.T) {
	stood := machine(nil)
	imaged(stood, false)

	said, err := stood.host().LoadImage(context.Background(), coordinate, strings.NewReader("tar-bytes"))
	if err != nil {
		t.Fatalf("LoadImage() = %v", err)
	}
	if !strings.Contains(said, coordinate) {
		t.Errorf("LoadImage() said %q, want what the daemon said it loaded", said)
	}
	if !strings.Contains(strings.Join(stood.carried(), "\n"), "tar-bytes") {
		t.Errorf("the tar never reached the machine: %v", stood.carried())
	}

	var loaded, checked bool
	for _, command := range stood.commands() {
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
		t.Errorf("a load that is never checked promotes a coordinate the machine may not answer to: %v", stood.commands())
	}
}

func TestALoadThatLeavesTheCoordinateUnansweredIsRefused(t *testing.T) {
	stood := machine(nil)
	stood.answer = func(command string) (session.Result, bool) {
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

	_, err := stood.host().LoadImage(context.Background(), coordinate, strings.NewReader("tar-bytes"))
	if err == nil {
		t.Fatal("LoadImage() succeeded where the machine answers to no such coordinate afterwards")
	}
	for _, named := range []string{coordinate, "content digest sha256:abc", "95%"} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("LoadImage() = %v, want %q in it: a load the box took and then does not hold explains itself with what the box's engine and disk say, or the cause is read off the wrong machine", err, named)
		}
	}
}

func refusedPull(said string) (*bench, *int) {
	stood := machine(nil)
	var asked int
	stood.answer = func(command string) (session.Result, bool) {
		if !strings.Contains(command, "docker pull") {
			return session.Result{}, false
		}
		asked++
		return session.Result{Code: 1, Stderr: said}, true
	}
	return stood, &asked
}

const (
	plainHex  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	markedHex = "4295030000000000000000000000000000000000000000000000000000000000"
)

func pulled(t *testing.T, stood *bench, server, hex string) error {
	t.Helper()
	_, err := stood.host().PullImage(context.Background(),
		providerkit.RegistryTarget{Server: server, Namespace: "acme"},
		server+"/acme/web:sha256-"+hex, "sha256:"+hex)
	return err
}

func TestAFatalPullIsNotAskedAgainBecauseTheRegistryAnswersOnPortFiveThousand(t *testing.T) {
	stood, asked := refusedPull("Error response from daemon: manifest unknown")

	if err := pulled(t, stood, "registry.example.com:5000", plainHex); err == nil {
		t.Fatal("PullImage() = nil over a machine whose daemon says the manifest is unknown")
	}
	if *asked != 1 {
		t.Errorf("the machine was told to pull %d times over a manifest that does not exist, want the refusal taken at its word: "+
			"the registry's own port is read as a 500 and every fatal pull is waited out five times", *asked)
	}
}

func TestAFatalPullIsNotAskedAgainBecauseTheDigestHexReadsAsAStatusCode(t *testing.T) {
	stood, asked := refusedPull("Error response from daemon: manifest unknown")

	if err := pulled(t, stood, "registry.example.com:9443", markedHex); err == nil {
		t.Fatal("PullImage() = nil over a machine whose daemon says the manifest is unknown")
	}
	if *asked != 1 {
		t.Errorf("the machine was told to pull %d times over a manifest that does not exist, want the refusal taken at its word: "+
			"the digest ocel pinned carries the hex 429 and 503, and a digest is random", *asked)
	}
}

func TestAThrottledPullIsStillAskedAgainWhereTheDaemonSaysSo(t *testing.T) {
	stood, asked := refusedPull("toomanyrequests: You have reached your pull rate limit")

	if err := pulled(t, stood, "registry.example.com:9443", plainHex); err == nil {
		t.Fatal("PullImage() = nil over a registry that throttled every pull")
	}
	if *asked != pullAttempts {
		t.Errorf("the machine was told to pull %d times over a registry that throttled it, want %d", *asked, pullAttempts)
	}
}

func TestALoginOutsideTheDockerGroupReachesTheDaemonAsRoot(t *testing.T) {
	stood := machine(nil)
	stood.facts = session.Facts{Systemd: true}
	stood.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker") && !strings.HasPrefix(command, "sudo -n ") {
			return session.Result{Code: 1, Stderr: "permission denied while trying to connect to the Docker daemon socket"}, true
		}
		if strings.Contains(command, "docker image ls") {
			return session.Result{Stdout: imageID + "\n"}, true
		}
		return session.Result{}, false
	}

	held, err := stood.host().HoldsImage(context.Background(), coordinate)
	if err != nil {
		t.Fatalf("HoldsImage() as a login the docker group does not hold = %v", err)
	}
	if !held {
		t.Error("HoldsImage() gave up on a login that cannot reach the socket unelevated rather than becoming root")
	}
}

func TestADaemonThatIsDownIsNotReadAsALoginOutsideTheDockerGroup(t *testing.T) {
	stood := machine(nil)
	stood.facts = session.Facts{Systemd: true}
	var probes int
	stood.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, dockerReach) {
			probes++
			return session.Result{Code: 1, Stderr: "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?"}, true
		}
		if strings.HasPrefix(command, "sudo -n ") {
			return session.Result{Code: 1, Stderr: "sudo: a password is required"}, true
		}
		return session.Result{}, false
	}
	h := stood.host()

	_, err := h.HoldsImage(context.Background(), coordinate)
	if err == nil {
		t.Fatal("HoldsImage() over a machine whose daemon is down succeeded")
	}
	if !strings.Contains(err.Error(), "Is the docker daemon running?") {
		t.Errorf("HoldsImage() = %v, want the reason the unelevated probe gave", err)
	}
	if strings.Contains(err.Error(), "password") {
		t.Errorf("HoldsImage() = %v: a daemon that is down is reported as a login that cannot become root", err)
	}

	if _, err := h.HoldsImage(context.Background(), coordinate); err == nil {
		t.Fatal("HoldsImage() = nil on a second ask over the same dead daemon")
	}
	if probes != 2 {
		t.Errorf("the daemon was probed %d times over two asks, want one probe each: a refusal that is cached leaves the deploy unable to recover once the daemon returns", probes)
	}
}

func TestTheDaemonIsFoundOnceHoweverManyImagesAreAskedAbout(t *testing.T) {
	stood := machine(nil)
	imaged(stood, true)
	h := stood.host()

	for range 3 {
		if _, err := h.HoldsImage(context.Background(), coordinate); err != nil {
			t.Fatal(err)
		}
	}
	var found int
	for _, command := range stood.commands() {
		if strings.Contains(command, dockerReach) {
			found++
		}
	}
	if found > 1 {
		t.Errorf("the daemon was looked for %d times over three questions about images: %v", found, stood.commands())
	}
}
