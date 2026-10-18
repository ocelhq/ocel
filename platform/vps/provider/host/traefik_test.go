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
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func traefikOnTheHost() front.Proxy {
	return front.Proxy{Traefik: &front.Traefik{
		Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt", PreviewResolver: "cloudflare",
		Entrypoints: &front.Entrypoints{HTTP: "web", HTTPS: "websecure"}, Port: 9000,
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

	opened, ok := coolifysTraefik().Front().Open(frontBox{}).(traefik.Traefik)
	if !ok {
		t.Fatalf("Coolify's Traefik opens as %T, want the traefik proxy", coolifysTraefik().Front().Open(frontBox{}))
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
	if got := flagged(board.Command, "--https-listen"); !slices.Equal(got, []string{"coolify:" + boxapi.HTTPSListenPort}) {
		t.Errorf("the switchboard serves as %q, want https heard on its own address on coolify alone", board.Command)
	}
	if got := flagged(board.Command, "--relay-network"); len(got) != 0 {
		t.Errorf("the switchboard serves as %q, want no network relayed from: every tenant on coolify would name its own scheme and host", board.Command)
	}
	running := shell.Words(board.Run())
	if !strings.Contains(running, shell.Words([]string{"--network", ProxyNetwork, "--network", "coolify"})) {
		t.Errorf("the switchboard runs as %s, want it started on coolify: the https listener binds its address there once, as it starts", running)
	}
	if strings.Contains(running, "--publish") {
		t.Errorf("the switchboard runs as %s, want nothing published: your Traefik reaches it on coolify", running)
	}
	if written := board.Writing(docker.ContainerRising); !strings.Contains(written, "proxy.traefik.network") {
		t.Errorf("the switchboard write refuses a missing network without naming the option that set it:\n%s", written)
	}
}

func TestTheSwitchboardBesideATraefikOnTheHostHearsHTTPSOnItsBoxNetworkAddressPublishedOnLoopback(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, traefikOnTheHost())
	if got := flagged(board.Command, "--https-listen"); !slices.Equal(got, []string{ProxyNetwork + ":" + boxapi.HTTPSListenPort}) {
		t.Errorf("the switchboard serves as %q, want https heard on its own address on %s, where the loopback port is published to", board.Command, ProxyNetwork)
	}
	if got := flagged(board.Command, "--relay-network"); len(got) != 0 {
		t.Errorf("the switchboard serves as %q, want no network relayed from", board.Command)
	}
	if running := shell.Words(board.Run()); !strings.Contains(running, shell.Words([]string{"--publish", "127.0.0.1:9000:" + boxapi.HTTPSListenPort})) {
		t.Errorf("the switchboard runs as %s, want the https listener published on 127.0.0.1:9000", running)
	}
}

func TestYourTraefikIsAskedWhetherItRoutesAHostnameWhateverCertificateItHasForItYet(t *testing.T) {
	t.Parallel()

	box := newBench(nil)
	if _, _, err := (frontBox{box.host()}).ProbeAnyCertificate(context.Background(), "web.localhost"); err != nil {
		t.Fatalf("Routed() = %v", err)
	}
	want := shell.Words([]string{boxapi.SwitchboardBinary, "probe", "--any-certificate", "web.localhost"})
	if box.at(want) < 0 {
		t.Errorf("Routed() ran %q, want %s: Traefik routes a hostname before it holds its certificate, and issuing waits on DNS", box.commands(), want)
	}
}

func TestTheLastDestroyBehindYourTraefikTakesOcelYmlOutOfItsDirectoryBeforeTheSwitchboard(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := newBench(map[environment.Tier][]machine.Item{tier: bootstrapped(t, tier)})
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
	if err := bootstrap.Remove(context.Background(), tier, progress.Discard()); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := box.commands()
	unplaced := slices.IndexFunc(taken, func(command string) bool {
		return strings.Contains(command, shell.Words(routing.Placement(coolifyDynamic, "unplace", coolifyFile)))
	})
	removed := slices.Index(taken, "docker rm --force "+shell.Quote(boxapi.Switchboard))
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

	for name, front := range map[string]front.Proxy{"a Traefik on the host": traefikOnTheHost(), "Coolify's Traefik on its network": coolifysTraefik()} {
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
	caddyIn := front.Caddy{Directory: root + "/caddy"}.Filled()
	for what, front := range map[string]front.Proxy{"Traefik": traefikIn(root + "/traefik"), "Caddy": {Caddy: &caddyIn}} {
		present := directoryPresent(front)
		said, err := exec.Command("sh", "-c", present).CombinedOutput()
		if err == nil || !strings.Contains(string(said), front.Front().DirectoryOption()) || !strings.Contains(string(said), root) {
			t.Errorf("finding %s's missing directory = %v, %q; want it refused naming %s and the directory", what, err, said, front.Front().DirectoryOption())
		}
		dir := filepath.Dir(resolvePlacedFile(front))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if said, err := exec.Command("sh", "-c", present).CombinedOutput(); err != nil {
			t.Errorf("finding %s's directory %s = %v, %q; want it found", what, dir, err, said)
		}
	}
	for what, front := range map[string]front.Proxy{"ocel's own proxy": {}, "a proxy routed by hand": routedByHand()} {
		if present := directoryPresent(front); present != "" {
			t.Errorf("bootstrap beside %s looks for a directory as\n%s\nwant none: nothing is placed", what, present)
		}
	}
}

func TestBootstrapOntoYourTraefikInADirectoryTheBoxLacksIsRefusedBeforeOcelsProxyIsTakenAway(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := boxRecordedFor(t, tier, front.Proxy{})
	prior := box.answer
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, "[ ! -d "+shell.Quote("/etc/traefik/dynamic")+" ]") {
			return ssh.Result{Code: 1, Stderr: "option \"proxy.traefik.directory\" names the directory /etc/traefik/dynamic, which this box does not have"}, true
		}
		return prior(command)
	}
	_, err := movedTo(t, box, tier, traefikOnTheHost())
	if err == nil || !strings.Contains(err.Error(), "proxy.traefik.directory") {
		t.Errorf("Apply() = %v, want it refused naming proxy.traefik.directory", err)
	}
	if at := box.at("docker rm --force " + shell.Quote(caddy.Container)); at >= 0 {
		t.Errorf("the bootstrap took ocel's proxy away before finding your Traefik has nowhere to read ocel's file: %s", box.commands()[at])
	}
}

func TestASwitchboardADeployRecreatesBesideYourTraefikHearsHTTPSWhereBootstrapHadIt(t *testing.T) {
	t.Parallel()

	restored := switchboardBox(nil, coolifysTraefik()).Restoring(docker.ContainerRising, routing.Locked("-x"))
	for _, want := range []string{
		shell.Words([]string{"--https-listen", "coolify:" + boxapi.HTTPSListenPort}),
		shell.Words([]string{"--network", ProxyNetwork, "--network", "coolify"}),
	} {
		if !strings.Contains(restored, want) {
			t.Errorf("a deploy recreates the switchboard as\n%s\nwithout %s", restored, want)
		}
	}
}

func TestYourTraefiksRoutersAreReadAsRootByALoginOutsideTheDockerGroup(t *testing.T) {
	rig := newBench(nil)
	rig.facts = ssh.Facts{Systemd: true}
	var unelevated []string
	rig.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, "docker") && !strings.HasPrefix(command, "sudo -n ") {
			unelevated = append(unelevated, command)
			return ssh.Result{Code: 1, Stderr: "permission denied while trying to connect to the Docker daemon socket"}, true
		}
		if strings.Contains(command, "beside") {
			return ssh.Result{Stdout: "[]"}, true
		}
		return ssh.Result{}, false
	}

	front := traefikOnTheHost().Front().Open(frontBox{rig.host()})
	if err := front.RefuseRouted(context.Background(), []string{"shop.example.com"}); err != nil {
		t.Errorf("RefuseRouted() as a login outside the docker group = %v, want the labels read as root", err)
	}
	if len(unelevated) > 1 {
		t.Errorf("the router scan ran %q without sudo, want only the probe that finds the socket denied", unelevated)
	}
}
