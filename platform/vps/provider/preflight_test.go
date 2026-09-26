package vps_test

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type answer struct {
	code   int
	stdout string
	stderr string
}

type scripted struct {
	mu     sync.Mutex
	ran    []string
	fed    []string
	answer func(command string) (answer, bool)
}

const (
	dataRoot     = "/var/lib/docker"
	roomySaid    = "root=" + dataRoot + "\nfree=104857600\nrepo=ocel-shop-web\nsize=100MB\n"
	crampedSaid  = "root=" + dataRoot + "\nfree=1024\nrepo=ocel-shop-web\n"
	deployedRepo = "ocel-shop-web"
	deployedRef  = deployedRepo + ":r0a1b2c3d"
)

type scriptedAnswer struct {
	naming string
	said   answer
}

func standingBox() []scriptedAnswer {
	return []scriptedAnswer{
		{"docker version", answer{}},
		{"docker info", answer{stdout: roomySaid}},
		{"docker inspect", answer{stdout: "Status=running ExitCode=0 OOMKilled=false Error= StartedAt=x FinishedAt= RestartCount=0"}},
		{"'upstreams'", answer{stdout: "[]\n"}},
		{"/proc/net/tcp &&", answer{stdout: socketTable(80, 443)}},
		{"publish=" + caddy.HTTPPort, answer{stdout: caddy.Container + "\n"}},
		{"publish=443", answer{stdout: caddy.Container + "\n"}},
		{"cat /proc/net/tcp\n", answer{stdout: ""}},
		{"'holds'", answer{}},
		{"proxy.json", answer{stdout: builtInRecord}},
	}
}

const builtInRecord = `{"proxy":null,"project":"shop","class":"production"}`

func boxSaying(overrides map[string]answer) *scripted {
	held := standingBox()
	for at, one := range held {
		if said, named := overrides[one.naming]; named {
			held[at].said = said
		}
	}
	for naming, said := range overrides {
		if !slices.ContainsFunc(held, func(one scriptedAnswer) bool { return one.naming == naming }) {
			held = append(held, scriptedAnswer{naming, said})
		}
	}
	return &scripted{answer: func(command string) (answer, bool) {
		for _, one := range held {
			if strings.Contains(command, one.naming) {
				return one.said, true
			}
		}
		return answer{}, false
	}}
}

func TestNoTwoThingsThisBenchScriptsAreNamedByTheSameCommand(t *testing.T) {
	t.Parallel()

	held := standingBox()
	for _, one := range held {
		for _, other := range held {
			if one.naming != other.naming && strings.Contains(one.naming, other.naming) {
				t.Errorf("this bench answers %q and %q, and the first holds the second: whichever is reached first wins, and a bench that answers a different command on a different run proves nothing about either",
					one.naming, other.naming)
			}
		}
	}
}

func (s *scripted) Stream(_ context.Context, command string, stdin io.Reader) (session.Result, error) {
	var carried string
	if stdin != nil {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return session.Result{}, err
		}
		carried = string(raw)
	}
	s.mu.Lock()
	s.ran = append(s.ran, command)
	s.fed = append(s.fed, carried)
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
			Class: edge.ClassProduction,
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
		"the switchboard's upstreams":        "'upstreams'",
		"the front proxy's admin socket":     "'holds'",
		"which container publishes port 80":  "publish=" + caddy.HTTPPort,
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
	for _, wanted := range []string{"guessed", dataRoot, deployedRepo} {
		if !strings.Contains(said, wanted) {
			t.Errorf("PreflightDeploy() = %q, want %q in it", said, wanted)
		}
	}
}

const (
	runningState    = "Status=running ExitCode=0 OOMKilled=false Error= StartedAt=x FinishedAt= RestartCount=0"
	exitedState     = "Status=exited ExitCode=1 OOMKilled=false Error= StartedAt=x FinishedAt=y RestartCount=0"
	restartingState = "Status=restarting ExitCode=1 OOMKilled=false Error= StartedAt=x FinishedAt=y RestartCount=9"
)

func proxyStates() map[string]map[string]answer {
	return map[string]map[string]answer{
		"the switchboard is not there at all": {
			"'upstreams'":    {code: 1, stderr: "Error: No such container: " + host.SwitchboardContainer},
			"docker inspect": {code: 1, stdout: "Error: No such object: " + host.SwitchboardContainer},
			"missing=":       {stdout: "missing=" + switchboard.FrontDir + "\nsum=" + boxBoardSum + "\n"},
		},
		"the switchboard exited": {
			"'upstreams'":    {code: 1, stderr: "Error response from daemon: container is not running"},
			"docker inspect": {stdout: exitedState},
		},
		"the switchboard is restarting": {
			"'upstreams'":    {code: 1, stderr: "Error response from daemon: container is restarting"},
			"docker inspect": {stdout: restartingState},
		},
		"the switchboard answers nothing over its control socket": {
			"'upstreams'":    {code: 1, stderr: "ocel-switchboard: the switchboard answered nothing over /run/ocel-switchboard/control.sock"},
			"docker inspect": {stdout: runningState},
		},
		"the front proxy is not there at all": {
			"'holds'":        {code: 1, stderr: "Error: No such container: " + caddy.Container},
			"docker inspect": {code: 1, stdout: "Error: No such object: " + caddy.Container},
		},
		"the front proxy exited": {
			"'holds'":        {code: 1, stderr: "Error response from daemon: container is not running"},
			"docker inspect": {stdout: exitedState},
		},
		"the front proxy has no admin socket": {
			"'holds'":        {code: 3, stderr: "ocel-switchboard: nothing answered on " + caddy.AdminSocket + ": dial unix " + caddy.AdminSocket + ": connect: no such file or directory"},
			"docker inspect": {stdout: runningState},
		},
		"the front proxy's admin socket is left behind and nothing answers on it": {
			"'holds'":        {code: 3, stderr: "ocel-switchboard: nothing answered on " + caddy.AdminSocket + ": dial unix " + caddy.AdminSocket + ": connect: connection refused"},
			"docker inspect": {stdout: runningState},
		},
		"the front proxy answers with no http app loaded": {
			"'holds'":        {code: 3, stderr: "ocel-switchboard: " + caddy.AdminSocket + " holds nothing at /config/apps/http"},
			"docker inspect": {stdout: runningState},
		},
	}
}

func TestEachContainerTheBoxServesThroughIsRefusedByNameAndByWhatIsWrongWithIt(t *testing.T) {
	t.Parallel()

	said := map[string]string{}
	for what, script := range proxyStates() {
		err := preflighting(boxSaying(script))
		if err == nil {
			t.Fatalf("PreflightDeploy() let a deploy past a box where %s, and a deploy into a box that cannot route or terminate is a green deploy nothing reaches", what)
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
	for what, wanted := range map[string][]string{
		"the switchboard is not there at all":                                     {host.SwitchboardContainer, "bootstrap", switchboard.FrontDir},
		"the switchboard exited":                                                  {host.SwitchboardContainer, "exited"},
		"the switchboard is restarting":                                           {host.SwitchboardContainer, "restarting"},
		"the switchboard answers nothing over its control socket":                 {host.SwitchboardContainer, "control socket"},
		"the front proxy is not there at all":                                     {caddy.Container, "bootstrap"},
		"the front proxy exited":                                                  {caddy.Container, "exited"},
		"the front proxy has no admin socket":                                     {caddy.Container, caddy.AdminSocket, "no such file"},
		"the front proxy's admin socket is left behind and nothing answers on it": {caddy.Container, caddy.AdminSocket, "connection refused"},
		"the front proxy answers with no http app loaded":                         {caddy.Container, "holds nothing"},
	} {
		for _, named := range wanted {
			if !strings.Contains(said[what], named) {
				t.Errorf("where %s the refusal is %q, want %q in it", what, said[what], named)
			}
		}
	}
}

func TestAForeignListenerOnAServingPortIsRefusedByName(t *testing.T) {
	t.Parallel()

	err := preflighting(boxSaying(map[string]answer{
		"publish=" + caddy.HTTPPort: {stdout: "not-ocels\n"},
	}))
	if err == nil {
		t.Fatal("PreflightDeploy() let a deploy onto a box where something else publishes port 80")
	}
	if !strings.Contains(err.Error(), "not-ocels") {
		t.Errorf("PreflightDeploy() = %q, want the container holding the port named", err)
	}
}

func TestAProcessOutsideDockerOnAServingPortIsRefusedByWhereItIsBound(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{
		"publish=" + caddy.HTTPPort: {stdout: "\n"},
		"cat /proc/net/tcp\n":       {stdout: socketTable(80)},
	})
	err := preflighting(machine)
	if err == nil || !strings.Contains(err.Error(), "port 80 is bound outside docker at 0.0.0.0:80") {
		t.Errorf("PreflightDeploy() = %v, want the port named by where it is bound", err)
	}
	for _, command := range machine.ran {
		if strings.Contains(command, listeners.SocketsMark) {
			t.Errorf("the deploy preflight walked every process on the box for the one behind the port:\n%s\nand the deploy login cannot read root's or www-data's descriptors, so it never names nginx", command)
		}
	}
}

func TestAServingPortNothingHoldsIsRefusedBecauseItMustBeTaken(t *testing.T) {
	t.Parallel()

	err := preflighting(boxSaying(map[string]answer{
		"publish=" + caddy.HTTPPort: {stdout: "\n"},
	}))
	if err == nil {
		t.Fatal("PreflightDeploy() read a free port 80 as fine, and on a bootstrapped box these ports are taken by the proxy rather than free")
	}
	if !strings.Contains(err.Error(), "nothing holds port "+caddy.HTTPPort) {
		t.Errorf("PreflightDeploy() = %q, want the port named as one nothing holds", err)
	}
}

const boxBoardSum = "5d1f0c9a2b7e4f6a8c3d1e0b9a7f5e3c1d2b4a6f8e0c2d4b6a8f0e2c4d6b8a0f"

func ahead(machine *scripted, fragment string, said answer) *scripted {
	asked := machine.answer
	machine.answer = func(command string) (answer, bool) {
		if strings.Contains(command, fragment) {
			return said, true
		}
		return asked(command)
	}
	return machine
}

func pruned(stood map[string]answer, restored answer) *scripted {
	return ahead(boxSaying(stood), "'docker' 'run'", restored)
}

func prunedBoard() map[string]answer {
	return map[string]answer{
		"'upstreams'":    {code: 1, stderr: "Error: No such container: " + host.SwitchboardContainer},
		"docker inspect": {code: 1, stdout: "Error: No such object: " + host.SwitchboardContainer},
		"missing=":       {stdout: "sum=" + boxBoardSum + "\n"},
	}
}

func ranWith(machine *scripted, fragment string) string {
	for _, command := range machine.ran {
		if strings.Contains(command, fragment) {
			return command
		}
	}
	return ""
}

func TestASwitchboardAPruneRemovedIsStoodAgainAndTheDeployGoesOn(t *testing.T) {
	t.Parallel()

	machine := pruned(prunedBoard(), answer{})
	if err := preflighting(machine); err != nil {
		t.Fatalf("PreflightDeploy() = %v, want a switchboard a prune removed stood again from what bootstrap left: Dokploy prunes every stopped container nightly, and the box still holds everything the switchboard is made of", err)
	}
	stood := ranWith(machine, "'docker' 'run'")
	if stood == "" {
		t.Fatalf("the preflight stood no container:\n%s", strings.Join(machine.ran, "\n"))
	}
	for what, fragment := range map[string]string{
		"the network it runs on, which the same prune takes": "docker network create 'ocel'",
		"its name": "'--name' 'ocel-switchboard'",
		"the label the next bootstrap reads its binary from":      "'ocel.config=" + boxBoardSum + "'",
		"the label the doctor reads the restoration from":         "'ocel.restored=",
		"its rejoining every project network that is still there": "docker network connect \"$net\" 'ocel-switchboard'",
	} {
		if !strings.Contains(stood, fragment) {
			t.Errorf("the switchboard was stood again without %s (%s):\n%s", what, fragment, stood)
		}
	}
	if strings.Contains(stood, "docker rm") {
		t.Errorf("the switchboard was stood again by removing whatever holds its name:\n%s\nwant it stood only while no container holds the name, or two deploys that both found it gone remove each other's", stood)
	}
	locked := strings.Index(stood, "flock -x 9\n")
	guarded := strings.Index(stood, "if ! docker inspect --type container --format '{{.Id}}' 'ocel-switchboard'")
	run := strings.Index(stood, "'docker' 'run'")
	unlocked := strings.Index(stood, "flock -u 9\n")
	closed := strings.LastIndex(stood[:max(unlocked, 0)], "\nfi\n")
	if locked < 0 || locked >= guarded || guarded >= run || run >= closed || closed >= unlocked {
		t.Errorf("the switchboard was stood again outside the routing lock or outside the check that no container holds its name:\n%s\nwant the lock taken, then the name checked, then the container run inside that check, then the lock let go: two deploys that both found it gone otherwise both run one, and the second fails on the name the first took", stood)
	}
}

func TestASwitchboardAPruneRemovedIsRefusedWhenWhatItMountsIsGoneToo(t *testing.T) {
	t.Parallel()

	for what, said := range map[string]string{
		"its front socket directory": "missing=" + switchboard.FrontDir + "\nsum=" + boxBoardSum + "\n",
		"its binary":                 "missing=" + host.SwitchboardBinary + "\n",
	} {
		script := prunedBoard()
		script["missing="] = answer{stdout: said}
		machine := pruned(script, answer{})
		err := preflighting(machine)
		if err == nil {
			t.Fatalf("PreflightDeploy() let a deploy past a box missing its switchboard and %s", what)
		}
		for _, wanted := range []string{host.SwitchboardContainer, "bootstrap", strings.TrimPrefix(strings.Fields(said)[0], "missing=")} {
			if !strings.Contains(err.Error(), wanted) {
				t.Errorf("without %s the refusal is %q, want %q in it", what, err, wanted)
			}
		}
		if stood := ranWith(machine, "'docker' 'run'"); stood != "" {
			t.Errorf("without %s the preflight still stood a switchboard, and docker creates a bind source that is not there as an empty root directory:\n%s", what, stood)
		}
	}
}

func TestASwitchboardThatCouldNotBeStoodAgainIsRefusedWithWhatTheBoxSaid(t *testing.T) {
	t.Parallel()

	err := preflighting(pruned(prunedBoard(), answer{code: 1, stderr: "gcr.io/distroless/static-debian12 was not pulled in 5 attempts"}))
	if err == nil {
		t.Fatal("PreflightDeploy() let a deploy past a switchboard it could not stand again")
	}
	for _, wanted := range []string{host.SwitchboardContainer, "not pulled", "bootstrap"} {
		if !strings.Contains(err.Error(), wanted) {
			t.Errorf("PreflightDeploy() = %q, want %q in it", err, wanted)
		}
	}
}
