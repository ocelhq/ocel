package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestEveryAppContainerJoinsANetworkNamedForItsTierAndProjectAndNeverTheProxysOwn(t *testing.T) {
	t.Parallel()

	production := valued()
	preview := valued()
	preview.Tier = environment.TierPreview
	other := valued()
	other.Project = "blog"

	networks := map[string]string{}
	for what, spec := range map[string]Container{"production shop": production, "preview shop": preview, "production blog": other} {
		argv := containerRun(spec, handedTo(spec))
		at := slices.Index(argv, "--network")
		if at < 0 || at+1 >= len(argv) {
			t.Fatalf("%s is run on no network at all: %v", what, argv)
		}
		networks[what] = argv[at+1]
		if argv[at+1] == ProxyNetwork {
			t.Errorf("%s is run on %s, the network every other project's containers sit on, so any of them reaches its :%s", what, ProxyNetwork, containerimage.PortText)
		}
		if argv[at+1] != live.AppNetwork(spec.Tier, spec.Project) {
			t.Errorf("%s is run on %q, want %q", what, argv[at+1], live.AppNetwork(spec.Tier, spec.Project))
		}
		if !slices.Contains(argv, LabelTier+"="+string(spec.Tier)) {
			t.Errorf("%s has no %s label, and a tier destroy enumerates what it started by that label: %v", what, LabelTier, argv)
		}
	}
	if networks["production shop"] == networks["preview shop"] {
		t.Errorf("shop's preview and production share %q, and a preview that reaches production's :%s is the flat network this replaces", networks["production shop"], containerimage.PortText)
	}
	if networks["production shop"] == networks["production blog"] {
		t.Errorf("shop and blog share %q, and one project reaching another's container is what a network per project exists to refuse", networks["production shop"])
	}
}

func TestRunningAContainerPutsItsNetworkAndTheProxyOnItBeforeTheRun(t *testing.T) {
	t.Parallel()

	spec := valued()
	box := runningWith(t, spec)
	joined := box.at(joinNetworkScript(spec.Tier, spec.Project))
	ran := box.at(quoted("run") + " " + quoted("--detach"))
	if joined < 0 || ran < 0 || joined > ran {
		t.Fatalf("the network was joined at %d and the container run at %d: a run onto a network that does not exist fails, and one the proxy is not on serves nothing", joined, ran)
	}
	script := joinNetworkScript(spec.Tier, spec.Project)
	network := quoted(live.AppNetwork(spec.Tier, spec.Project))
	for what, wanted := range map[string]string{
		"a create that sets the tier label":     quoted(LabelTier + "=" + string(spec.Tier)),
		"a create that sets the project label":  quoted(LabelProject + "=shop"),
		"the create itself":                     "docker network create",
		"the proxy attached to it":              "docker network connect " + network + " " + quoted(SwitchboardContainer),
		"a create that tolerates a neighbour's": "&& ! docker network inspect " + network,
	} {
		if !strings.Contains(script, wanted) {
			t.Errorf("joining the network runs\n%s\nwhich contains no %s (%s)", script, what, wanted)
		}
	}
	for _, refused := range []string{"--internal", "--icc", "enable_icc"} {
		if strings.Contains(script, refused) {
			t.Errorf("joining the network runs %q, and %s either cuts the app off the internet or cuts the proxy off the app", script, refused)
		}
	}
}

func TestAnEngineOutOfSubnetsIsRefusedWithTheDaemonSettingThatGivesItMore(t *testing.T) {
	t.Parallel()

	spec := valued()
	box := machine(nil)
	imaging(box, "false ")
	proxied := box.answer
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker network create") {
			return session.Result{Code: 1, Stderr: "could not find an available, non-overlapping IPv4 address pool among the defaults to assign to the network"}, true
		}
		return proxied(command)
	}
	err := box.host().RunContainer(context.Background(), spec)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("RunContainer() on an engine with no subnet left = %v, want a not-ready refusal", err)
	}
	for _, wanted := range []string{"default-address-pools", "/etc/docker/daemon.json", live.AppNetwork(spec.Tier, spec.Project)} {
		if !strings.Contains(err.Error(), wanted) {
			t.Errorf("the refusal reads %q and never names %q", err, wanted)
		}
	}
	if box.at(quoted("run")+" "+quoted("--detach")) >= 0 {
		t.Error("a container was run onto a network that was never created")
	}
	if box.at("install -m 0600") >= 0 {
		t.Error("the env file was written for a container that could never be run")
	}
}

func dockerStubbing(t *testing.T, script string) string {
	t.Helper()
	stub := t.TempDir()
	executable(t, filepath.Join(stub, dockerEngine), "#!/bin/sh\n"+script)
	return stub
}

func TestAProjectsNetworkIsForgottenOnlyOnceNothingButTheProxyIsOnIt(t *testing.T) {
	t.Parallel()

	tier, project := environment.TierProduction, "shop"
	for members, want := range map[string]string{
		SwitchboardContainer + "\n":                "",
		SwitchboardContainer + "\nshop-web-1111\n": networkInUse,
		"shop-web-1111\n":                          networkInUse,
		"":                                         "",
	} {
		log := filepath.Join(t.TempDir(), "log")
		stub := dockerStubbing(t, "printf '%s\\n' \"$*\" >>"+log+"\n"+
			"case \"$1 $2\" in\n"+
			"'network inspect') if [ \"$3\" = --format ]; then printf '%s' "+quoted(members)+"; fi; exit 0 ;;\n"+
			"'network disconnect'|'network rm') exit 0 ;;\n"+
			"esac\nexit 1\n")
		run := exec.Command("/bin/sh", "-c", networkForgetting(tier, project))
		run.Env = []string{"PATH=" + stub + ":" + os.Getenv("PATH")}
		rendered, err := run.Output()
		if err != nil {
			t.Fatalf("forgetting the network with %q on it = %v", members, err)
		}
		if strings.TrimSpace(string(rendered)) != want {
			t.Errorf("forgetting the network with %q on it answers %q, want %q", members, strings.TrimSpace(string(rendered)), want)
		}
		ran, _ := os.ReadFile(log)
		removed := strings.Contains(string(ran), "network rm ")
		if removed == (want == networkInUse) {
			t.Errorf("forgetting the network with %q on it ran\n%s\nand a network a container still sits on is one that container reaches nothing without", members, ran)
		}
	}
}

func TestATierDestroyTakesTheContainersAndNetworksItLabelledAndTheProxyOffThemFirst(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, quoted("label="+tierSelector(tier))) && strings.Contains(command, "echo containers") {
			return session.Result{Stdout: "containers\nnetworks\n"}, true
		}
		return session.Result{}, false
	}
	progress := &said{}
	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, progress); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	for _, taken := range []string{
		"Removed the app containers labelled " + tierSelector(tier),
		"Removed the app networks labelled " + tierSelector(tier),
	} {
		if !slices.Contains(progress.lines, taken) {
			t.Errorf("Remove() never said %q:\n%s", taken, strings.Join(progress.lines, "\n"))
		}
	}
	containers := box.at("xargs -r docker rm --force")
	networks := box.at("docker network disconnect --force")
	state := box.at("rm -rf " + quoted(StateDir(tier)))
	if containers < 0 || networks < 0 || containers > networks || networks > state {
		t.Errorf("the containers went at %d, the networks at %d and the state at %d: a network goes after every container on it, the proxy is detached before the rm, and the values a container was handed go before the records that sealed them", containers, networks, state)
	}
	for _, command := range box.taking() {
		if strings.Contains(command, "docker network rm") && !strings.Contains(command, quoted("label="+tierSelector(tier))) && !strings.Contains(command, quoted(ProxyNetwork)) {
			t.Errorf("Remove() ran %q, which names a network ocel did not label", command)
		}
		if strings.Contains(command, "docker rm --force") && !strings.Contains(command, quoted("label="+tierSelector(tier))) &&
			!strings.Contains(command, quoted(SwitchboardContainer)) && !strings.Contains(command, quoted(caddy.Container)) {
			t.Errorf("Remove() ran %q, which names a container ocel did not label", command)
		}
	}
}

func TestATierRunningNothingPlansNoContainerOrNetworkRemoval(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})
	plan, err := NewBootstrap(box.host(), testVendor, "shop").PlanRemove(context.Background(), tier)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Kind == KindApps || change.Kind == KindAppNetworks {
				t.Errorf("a tier the engine reports nothing labelled for plans %s %s, and a plan naming what does not exist is one the user cannot read", change.Kind, change.Name)
			}
		}
	}
}

func TestTheSwitchboardJoinsEveryLabelledNetworkBeforeItStartsServing(t *testing.T) {
	t.Parallel()

	board := switchboardBox(nil, Front{})
	for name, command := range map[string]string{
		"written again": board.writing(containerRising),
		"restored":      board.restoring(containerRising),
	} {
		created := strings.Index(command, quoted("docker")+" "+quoted("create"))
		rejoin := strings.Index(command, "docker network ls --quiet --filter "+quoted("label="+LabelTier))
		started := strings.Index(command, "docker start "+quoted(SwitchboardContainer))
		rising := strings.Index(command, "while :; do")
		if created < 0 || rejoin < created || started < rejoin || rising < started {
			t.Errorf("the switchboard %s is created at %d, rejoins at %d, starts at %d and is waited on at %d: a switchboard that serves before it is back on a project's network answers that project 502 until it is:\n%s",
				name, created, rejoin, started, rising, command)
		}
		if !strings.Contains(command, "docker network connect \"$net\" "+quoted(SwitchboardContainer)+" >/dev/null\n") {
			t.Errorf("the switchboard %s rejoins with a connect whose failure is swallowed, and a switchboard off a project's network serves that project nothing:\n%s", name, command)
		}
	}
}

func TestASwitchboardWrittenAgainIsStoppedToFinishWhatItServesBeforeItIsRemoved(t *testing.T) {
	t.Parallel()

	command := switchboardBox(nil, Front{}).writing(containerRising)
	stopped := strings.Index(command, "docker stop "+quoted(SwitchboardContainer))
	removed := strings.Index(command, "docker rm --force "+quoted(SwitchboardContainer))
	if stopped < 0 || removed < stopped {
		t.Errorf("the switchboard written again is stopped at %d and removed at %d: removed while it runs, it is killed with the requests it holds open, and each answers your proxy with a reset:\n%s", stopped, removed, command)
	}
}

func TestTheProxysNetworkFactReadsItsOwnMembershipAndNotTheNetworksDeploysAttachIt(t *testing.T) {
	t.Parallel()

	if strings.Contains(ContainerFactTemplate, "range $n, $v := .NetworkSettings.Networks") {
		t.Fatalf("the proxy's facts list every network it is on, so every deploy that attaches it to a project's network reads as drift a bootstrap would recreate the proxy over:\n%s", ContainerFactTemplate)
	}
	if !strings.Contains(ContainerFactTemplate, `index .NetworkSettings.Networks "`+ProxyNetwork+`"`) {
		t.Errorf("the proxy's facts never ask whether it sits on %s:\n%s", ProxyNetwork, ContainerFactTemplate)
	}
}

func TestRunningAResourcePutsTheProxyOnItsNetworkBeforeTheRun(t *testing.T) {
	t.Parallel()

	spec := resourced()
	box := machine(nil)
	if err := box.host().RunResource(context.Background(), spec, "secret"); err != nil {
		t.Fatalf("RunResource() = %v", err)
	}
	joined := box.at("docker network connect " + quoted(live.AppNetwork(spec.Tier, spec.Project)) + " " + quoted(SwitchboardContainer))
	ran := box.at(quoted("run") + " " + quoted("--detach"))
	if joined < 0 || ran < 0 || joined > ran {
		t.Fatalf("the proxy joined %s at %d and %s ran at %d: a store is routed as soon as it runs, and a proxy off its network resolves no upstream until some app of the project deploys: %v",
			live.AppNetwork(spec.Tier, spec.Project), joined, spec.Name, ran, box.commands())
	}
}

func TestAResourceAlreadyRunningStillPutsTheSwitchboardBackOnItsNetwork(t *testing.T) {
	t.Parallel()

	spec := resourced()
	digest, err := spec.digest()
	if err != nil {
		t.Fatal(err)
	}
	box := machine(nil)
	imaging(box, "running "+spec.Image+" "+digest)
	if err := box.host().RunResource(context.Background(), spec, "secret"); err != nil {
		t.Fatalf("RunResource() = %v", err)
	}
	if box.at(quoted("run")+" "+quoted("--detach")) >= 0 {
		t.Fatalf("a resource already serving its image was started again: %v", box.commands())
	}
	if box.at("docker network connect "+quoted(live.AppNetwork(spec.Tier, spec.Project))+" "+quoted(SwitchboardContainer)) < 0 {
		t.Errorf("a deploy over a resource already running never put %s on %s: a project with only resources is one no app deploy rejoins, so a switchboard left off it routes that project's store to nothing for good: %v",
			SwitchboardContainer, live.AppNetwork(spec.Tier, spec.Project), box.commands())
	}
}

func TestAnAppAlreadyServingStillPutsTheSwitchboardBackOnItsNetwork(t *testing.T) {
	t.Parallel()

	serving := "running " + appImage + " " + handedTo(aContainer()).digest
	for what, spec := range map[string]Container{
		"a deploy":    aContainer(),
		"a promotion": {Name: physical, Project: "shop", App: "web", Image: appImage},
	} {
		box := machine(nil)
		imaging(box, serving)
		if err := box.host().RunContainer(context.Background(), spec); err != nil {
			t.Fatalf("%s: RunContainer() = %v", what, err)
		}
		if box.at(quoted("run")+" "+quoted("--detach")) >= 0 {
			t.Fatalf("%s of an app already serving started it again: %v", what, box.commands())
		}
		if box.at("docker network connect "+quoted(live.AppNetwork(spec.Tier, spec.Project))+" "+quoted(SwitchboardContainer)) < 0 {
			t.Errorf("%s of an app already serving never put %s on %s, so a switchboard left off it is never repaired: %v",
				what, SwitchboardContainer, live.AppNetwork(spec.Tier, spec.Project), box.commands())
		}
	}
}
