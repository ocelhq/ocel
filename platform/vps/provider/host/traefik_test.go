package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func traefikOnTheHost() Front {
	return Front{Traefik: &TraefikFront{
		Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt", PreviewResolver: "cloudflare",
		Entrypoints: Entrypoints{HTTP: "web", HTTPS: "websecure"}, Port: 9000,
	}}
}

func flagged(command []string, flag string) []string {
	var values []string
	for at, arg := range command {
		if arg == flag && at+1 < len(command) {
			values = append(values, command[at+1])
		}
	}
	return values
}

func TestYourTraefikOpensAsTheTraefikProxyWithEverythingTheOptionFills(t *testing.T) {
	t.Parallel()

	opened, ok := openFront(coolifysTraefik(), frontBox{}).(traefik.Traefik)
	if !ok {
		t.Fatalf("Coolify's Traefik opens as %T, want the traefik proxy", openFront(coolifysTraefik(), frontBox{}))
	}
	want := traefik.Traefik{Box: frontBox{}, Directory: "/data/coolify/proxy/dynamic", ContainerDirectory: "/traefik/dynamic", Resolver: "letsencrypt", HTTP: "http", HTTPS: "https", Network: "coolify"}
	if opened != want {
		t.Errorf("Coolify's Traefik opens as %+v, want %+v", opened, want)
	}
	if file := opened.File(); file != "/data/coolify/proxy/dynamic/ocel.yml" {
		t.Errorf("File() = %q, want ocel.yml in Coolify's dynamic directory", file)
	}
}

func TestTheSwitchboardBesideYourTraefikOnItsNetworkHearsHTTPSThereAloneAndStartsOnIt(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, coolifysTraefik())
	if got := flagged(board.command, "--https-listen"); !slices.Equal(got, []string{"coolify:" + switchboard.HTTPSListenPort}) {
		t.Errorf("the switchboard serves as %q, want https heard on its own address on coolify alone", board.command)
	}
	if got := flagged(board.command, "--relay-network"); len(got) != 0 {
		t.Errorf("the switchboard serves as %q, want no network relayed from: every tenant on coolify would name its own scheme and host", board.command)
	}
	running := words(board.run())
	if !strings.Contains(running, words([]string{"--network", ProxyNetwork, "--network", "coolify"})) {
		t.Errorf("the switchboard runs as %s, want it started on coolify: the https listener binds its address there once, as it starts", running)
	}
	if strings.Contains(running, "--publish") {
		t.Errorf("the switchboard runs as %s, want nothing published: your Traefik reaches it on coolify", running)
	}
	if written := board.writing(containerRising); !strings.Contains(written, "proxy.traefik.network") {
		t.Errorf("the switchboard write refuses a missing network without naming the option that set it:\n%s", written)
	}
}

func TestTheSwitchboardBesideATraefikOnTheHostHearsHTTPSOnItsBoxNetworkAddressPublishedOnLoopback(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, traefikOnTheHost())
	if got := flagged(board.command, "--https-listen"); !slices.Equal(got, []string{ProxyNetwork + ":" + switchboard.HTTPSListenPort}) {
		t.Errorf("the switchboard serves as %q, want https heard on its own address on %s, where the loopback port is published to", board.command, ProxyNetwork)
	}
	if got := flagged(board.command, "--relay-network"); len(got) != 0 {
		t.Errorf("the switchboard serves as %q, want no network relayed from", board.command)
	}
	if running := words(board.run()); !strings.Contains(running, words([]string{"--publish", "127.0.0.1:9000:" + switchboard.HTTPSListenPort})) {
		t.Errorf("the switchboard runs as %s, want the https listener published on 127.0.0.1:9000", running)
	}
}

func TestYourTraefikIsAskedWhetherItRoutesAHostnameWhateverCertificateItHasForItYet(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	if _, _, err := (frontBox{box.host()}).ProbeAnyCertificate(context.Background(), "web.localhost"); err != nil {
		t.Fatalf("Routed() = %v", err)
	}
	want := words([]string{SwitchboardBinary, "probe", "--any-certificate", "web.localhost"})
	if box.at(want) < 0 {
		t.Errorf("Routed() ran %q, want %s: Traefik routes a hostname before it holds its certificate, and issuing waits on DNS", box.commands(), want)
	}
}

func TestTheLastDestroyBehindYourTraefikTakesOcelYmlOutOfItsDirectoryBeforeTheSwitchboard(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})
	bootstrap := NewBootstrap(box.fronted(coolifysTraefik()), testVendor, "shop")
	plan, err := bootstrap.PlanRemove(context.Background(), tier)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	if !slices.ContainsFunc(plan.Groups[0].Changes, func(change provider.Change) bool {
		return change.Name == coolifyFile && change.Action == provider.ActionDelete
	}) {
		t.Errorf("the removal plan %+v leaves %s behind in your Traefik's directory", plan.Groups[0].Changes, coolifyFile)
	}
	if err := bootstrap.Remove(context.Background(), tier, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := box.commands()
	unplaced := slices.IndexFunc(taken, func(command string) bool {
		return strings.Contains(command, words(placementCommand(coolifyDynamic, "unplace", coolifyFile)))
	})
	removed := slices.Index(taken, "docker rm --force "+quoted(SwitchboardContainer))
	if unplaced < 0 || removed < 0 || unplaced > removed {
		t.Errorf("the destroy ran\n%s\nwant %s unplaced before the switchboard is removed: Traefik would route ocel's hostnames to a service that is gone", strings.Join(taken, "\n"), coolifyFile)
	}
}

func TestABootstrapBehindATraefikInABridgedContainerWithNoNetworkNamedIsRefusedNamingNetwork(t *testing.T) {
	t.Parallel()

	rig := withDocker()
	portsOwnedOn(rig, map[string]string{"443": "traefik\n"})
	refusedBeforeWriting(t, rig, traefikOnTheHost(),
		"container traefik publishes :443 from a docker network, where 127.0.0.1:9000 is its own loopback and not this box's, so it cannot reach ocel's switchboard\n"+
			"Set `proxy.traefik.network` to the docker network traefik is on, or run traefik with network_mode: host")
}

func TestABootstrapBehindATraefikOnTheHostsOwnNetworkGoesAhead(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]Front{"a Traefik on the host": traefikOnTheHost(), "Coolify's Traefik on its network": coolifysTraefik()} {
		rig := withDocker()
		published := "\n"
		if front.Traefik.Network != "" {
			published = "coolify-proxy\n"
		}
		portsOwnedOn(rig, map[string]string{"443": published}, socketOwner{443, "traefik"})
		if _, err := NewBootstrap(rig.fronted(front), testVendor, "shop").Plan(context.Background(),
			provider.BootstrapRequest{Tier: environment.TierProduction}); err != nil {
			t.Errorf("%s: Plan() = %v, want the bootstrap let through", name, err)
		}
	}
}

func TestBootstrapBesideYourProxyFindsTheDirectoryItPlacesInAndNamesTheOptionWhenTheBoxHasNone(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	caddyIn := CaddyFront{Directory: root + "/caddy"}.Filled()
	for what, front := range map[string]Front{"Traefik": traefikIn(root + "/traefik"), "Caddy": {Caddy: &caddyIn}} {
		present := front.directoryPresent()
		said, err := exec.Command("sh", "-c", present).CombinedOutput()
		if err == nil || !strings.Contains(string(said), front.directoryOption()) || !strings.Contains(string(said), root) {
			t.Errorf("finding %s's missing directory = %v, %q; want it refused naming %s and the directory", what, err, said, front.directoryOption())
		}
		dir := filepath.Dir(front.resolvePlacedFile())
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if said, err := exec.Command("sh", "-c", present).CombinedOutput(); err != nil {
			t.Errorf("finding %s's directory %s = %v, %q; want it found", what, dir, err, said)
		}
	}
	for what, front := range map[string]Front{"ocel's own proxy": {}, "a proxy routed by hand": routedByHand()} {
		if present := front.directoryPresent(); present != "" {
			t.Errorf("bootstrap beside %s looks for a directory as\n%s\nwant none: nothing is placed", what, present)
		}
	}
}

func TestBootstrapOntoYourTraefikInADirectoryTheBoxLacksIsRefusedBeforeOcelsProxyIsTakenAway(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := boxRecordedFor(t, tier, Front{})
	prior := box.answer
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "[ ! -d "+quoted("/etc/traefik/dynamic")+" ]") {
			return session.Result{Code: 1, Stderr: "option \"proxy.traefik.directory\" names the directory /etc/traefik/dynamic, which this box does not have"}, true
		}
		return prior(command)
	}
	_, err := movedTo(t, box, tier, traefikOnTheHost())
	if err == nil || !strings.Contains(err.Error(), "proxy.traefik.directory") {
		t.Errorf("Apply() = %v, want it refused naming proxy.traefik.directory", err)
	}
	if at := box.at("docker rm --force " + quoted(caddy.Container)); at >= 0 {
		t.Errorf("the bootstrap took ocel's proxy away before finding your Traefik has nowhere to read ocel's file: %s", box.commands()[at])
	}
}

func TestASwitchboardADeployRecreatesBesideYourTraefikHearsHTTPSWhereBootstrapHadIt(t *testing.T) {
	t.Parallel()

	restored := switchboardBox(nil, coolifysTraefik()).restoring(containerRising)
	for _, want := range []string{
		words([]string{"--https-listen", "coolify:" + switchboard.HTTPSListenPort}),
		words([]string{"--network", ProxyNetwork, "--network", "coolify"}),
	} {
		if !strings.Contains(restored, want) {
			t.Errorf("a deploy recreates the switchboard as\n%s\nwithout %s", restored, want)
		}
	}
}

func TestYourTraefiksRoutersAreReadAsRootByALoginOutsideTheDockerGroup(t *testing.T) {
	rig := machine(nil)
	rig.facts = session.Facts{Systemd: true}
	var unelevated []string
	rig.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker") && !strings.HasPrefix(command, "sudo -n ") {
			unelevated = append(unelevated, command)
			return session.Result{Code: 1, Stderr: "permission denied while trying to connect to the Docker daemon socket"}, true
		}
		if strings.Contains(command, "beside") {
			return session.Result{Stdout: "[]"}, true
		}
		return session.Result{}, false
	}

	front := openFront(traefikOnTheHost(), frontBox{rig.host()})
	if err := front.RefuseRouted(context.Background(), []string{"shop.example.com"}); err != nil {
		t.Errorf("RefuseRouted() as a login outside the docker group = %v, want the labels read as root", err)
	}
	if len(unelevated) > 1 {
		t.Errorf("the router scan ran %q without sudo, want only the probe that finds the socket denied", unelevated)
	}
}
