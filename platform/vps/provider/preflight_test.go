package vps_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type answer struct {
	code   int
	stdout string
	stderr string
}

type scripted struct {
	mu     sync.Mutex
	ran    []string
	answer func(command string) (answer, bool)
}

const (
	dataRoot     = "/var/lib/docker"
	roomySaid    = "root=" + dataRoot + "\nfree=104857600\nrepo=ocel-shop-web\nsize=104857600\n"
	crampedSaid  = "root=" + dataRoot + "\nfree=1024\nrepo=ocel-shop-web\n"
	deployedRepo = "ocel-shop-web"
	deployedRef  = deployedRepo + ":r0a1b2c3d"
)

var standingBox = map[string]answer{
	"docker version":              {},
	"docker info":                 {stdout: roomySaid},
	"'upstreams'":                 {stdout: "[]\n"},
	"publish=" + host.RenewalPort: {stdout: host.ProxyContainer + "\n"},
	"publish=443":                 {stdout: host.ProxyContainer + "\n"},
	"'listeners'":                 {stdout: "0.0.0.0:80\n"},
	"cat /proc/net/tcp":           {stdout: ""},
	"test -x":                     {},
	"test -S":                     {},
}

func boxSaying(overrides map[string]answer) *scripted {
	held := map[string]answer{}
	for key, said := range standingBox {
		held[key] = said
	}
	for key, said := range overrides {
		held[key] = said
	}
	return &scripted{answer: func(command string) (answer, bool) {
		for key, said := range held {
			if strings.Contains(command, key) {
				return said, true
			}
		}
		return answer{}, false
	}}
}

func (s *scripted) Stream(_ context.Context, command string, _ io.Reader) (session.Result, error) {
	s.mu.Lock()
	s.ran = append(s.ran, command)
	s.mu.Unlock()
	if said, held := s.answer(command); held {
		return session.Result{Code: said.code, Stdout: said.stdout, Stderr: said.stderr}, nil
	}
	return session.Result{}, nil
}

func (s *scripted) Run(ctx context.Context, command string) (string, error) {
	result, err := s.Stream(ctx, command, nil)
	return result.Stdout, err
}

func (s *scripted) Preflight(context.Context) (session.Facts, error) {
	return session.Facts{Root: true, Systemd: true}, nil
}

func (s *scripted) Destination() session.Destination {
	return session.Destination{Written: "box.invalid", Address: "box.invalid", Port: 22, User: "ada"}
}

func preflighting(machine *scripted) error {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		return err
	}
	return p.PreflightDeploy(context.Background(), providerkit.DeployPreflight{
		Plan: providerkit.DeployPlan{
			Slug:  "shop",
			Class: providerkit.ClassProduction,
			Apps:  []providerkit.AppEntry{{App: "web", Stack: stack, Image: deployedRef}},
		},
	})
}

func TestABoxThatIsReadyRefusesNothingBeforeADeploy(t *testing.T) {
	t.Parallel()

	machine := boxSaying(nil)
	if err := preflighting(machine); err != nil {
		t.Fatalf("PreflightDeploy() = %v, want a box that answers every check to be let through", err)
	}
	joined := strings.Join(machine.ran, "\n")
	for what, fragment := range map[string]string{
		"the engine answering as this login": "docker version",
		"what the docker data root has left": "docker info",
		"the one read-only admin call":       "'upstreams'",
		"which container publishes port 80":  "publish=" + host.RenewalPort,
		"which container publishes port 443": "publish=443",
	} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("a preflight over a standing box never asked about %s:\n%s", what, joined)
		}
	}
}

func TestAnEngineThatRefusesThisLoginIsRefusedBeforeAnythingIsStreamed(t *testing.T) {
	t.Parallel()

	err := preflighting(boxSaying(map[string]answer{
		"docker version": {code: 1, stderr: "permission denied while trying to connect to the Docker daemon socket"},
	}))
	if err == nil {
		t.Fatal("PreflightDeploy() let a deploy past an engine that refuses this login, and it would surface as a failed image load halfway through the transfer")
	}
	for _, wanted := range []string{"docker", "group", host.DeployUser()} {
		if !strings.Contains(err.Error(), wanted) {
			t.Errorf("PreflightDeploy() = %q, want %q named as the remedy", err, wanted)
		}
	}
}

func TestADiskWithoutRoomForTheWindowRefusesAndNamesTheGuess(t *testing.T) {
	t.Parallel()

	err := preflighting(boxSaying(map[string]answer{"docker info": {stdout: crampedSaid}}))
	if err == nil {
		t.Fatal("PreflightDeploy() let a deploy onto a disk with a kibibyte free, and a disk that fills mid-transfer fails mid-transfer")
	}
	said := err.Error()
	for _, wanted := range []string{"guessed constant", dataRoot, deployedRepo} {
		if !strings.Contains(said, wanted) {
			t.Errorf("PreflightDeploy() = %q, want %q in it", said, wanted)
		}
	}
}

func proxyStates() map[string]map[string]answer {
	return map[string]map[string]answer{
		"the container is not there at all": {
			"'upstreams'":    {code: 1, stderr: "Error: No such container: " + host.ProxyContainer},
			"docker inspect": {code: 1, stdout: "Error: No such object: " + host.ProxyContainer},
		},
		"the container exited": {
			"'upstreams'":    {code: 1, stderr: "Error response from daemon: container is not running"},
			"docker inspect": {stdout: "Status=exited ExitCode=1 OOMKilled=false Error= StartedAt=x FinishedAt=y RestartCount=0"},
		},
		"the container is restarting": {
			"'upstreams'":    {code: 1, stderr: "Error response from daemon: container is restarting"},
			"docker inspect": {stdout: "Status=restarting ExitCode=1 OOMKilled=false Error= StartedAt=x FinishedAt=y RestartCount=9"},
		},
		"the flip helper is not executable": {
			"'upstreams'":    {code: 1, stderr: "exec: \"" + host.ProxyHelperMount + "\": permission denied"},
			"docker inspect": {stdout: "Status=running ExitCode=0 OOMKilled=false Error= StartedAt=x FinishedAt= RestartCount=0"},
			"test -x":        {code: 1, stderr: "no such file"},
		},
		"the admin socket is not there": {
			"'upstreams'":    {code: 1, stderr: "ocel-proxyctl: the proxy answered nothing over " + host.ProxyAdminSocket},
			"docker inspect": {stdout: "Status=running ExitCode=0 OOMKilled=false Error= StartedAt=x FinishedAt= RestartCount=0"},
			"test -S":        {code: 1, stderr: "no such file"},
		},
		"the admin socket is there and refusing": {
			"'upstreams'":    {code: 1, stderr: "ocel-proxyctl: the proxy answered /reverse_proxy/upstreams with 403 Forbidden"},
			"docker inspect": {stdout: "Status=running ExitCode=0 OOMKilled=false Error= StartedAt=x FinishedAt= RestartCount=0"},
		},
	}
}

func TestTheProxyStatesAreSixDistinctRefusalsAndNoneOfThemSaysOnlyThatItIsDown(t *testing.T) {
	t.Parallel()

	said := map[string]string{}
	for what, script := range proxyStates() {
		err := preflighting(boxSaying(script))
		if err == nil {
			t.Fatalf("PreflightDeploy() let a deploy past a box where %s, and a deploy into a box whose proxy cannot be flipped is a green deploy nothing routes to", what)
		}
		said[what] = err.Error()
	}
	for what, message := range said {
		for other, held := range said {
			if other != what && held == message {
				t.Errorf("%q and %q are refused with the same words, and they have different fixes:\n%s", what, other, message)
			}
		}
	}
	for what, wanted := range map[string]string{
		"the container is not there at all":      "bootstrap",
		"the container exited":                   "exited",
		"the container is restarting":            "restarting",
		"the flip helper is not executable":      host.ProxyHelperMount,
		"the admin socket is not there":          "no socket at " + host.ProxyAdminSocket,
		"the admin socket is there and refusing": "refused the one read",
	} {
		if !strings.Contains(said[what], wanted) {
			t.Errorf("where %s the refusal is %q, want %q in it", what, said[what], wanted)
		}
	}
}

func TestAForeignListenerOnAServingPortIsRefusedByName(t *testing.T) {
	t.Parallel()

	err := preflighting(boxSaying(map[string]answer{
		"publish=" + host.RenewalPort: {stdout: "not-ocels\n"},
	}))
	if err == nil {
		t.Fatal("PreflightDeploy() let a deploy onto a box where something else publishes port 80")
	}
	if !strings.Contains(err.Error(), "not-ocels") {
		t.Errorf("PreflightDeploy() = %q, want the container holding the port named", err)
	}
}

func TestAServingPortNothingHoldsIsRefusedBecauseItMustBeTaken(t *testing.T) {
	t.Parallel()

	err := preflighting(boxSaying(map[string]answer{
		"publish=" + host.RenewalPort: {stdout: "\n"},
	}))
	if err == nil {
		t.Fatal("PreflightDeploy() read a free port 80 as fine, and on a bootstrapped box these ports are taken by the proxy rather than free")
	}
	if !strings.Contains(err.Error(), "nothing on this host holds port "+host.RenewalPort) {
		t.Errorf("PreflightDeploy() = %q, want the port named as one nothing holds", err)
	}
}
