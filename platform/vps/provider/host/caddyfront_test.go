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
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func coolifysCaddy() Front {
	filled := CaddyFront{Preset: "coolify"}.Filled()
	return Front{Caddy: &filled}
}

func caddyService() Front {
	filled := CaddyFront{Directory: "/etc/caddy/ocel.d"}.Filled()
	return Front{Caddy: &filled}
}

func TestACaddyOptionOpensTheCaddyfileProxyOverTheDirectoryItImports(t *testing.T) {
	t.Parallel()

	opened, ok := openFront(coolifysCaddy(), frontBox{}).(caddyfile.Caddyfile)
	if !ok {
		t.Fatalf("openFront() = %T, want caddyfile.Caddyfile", openFront(coolifysCaddy(), frontBox{}))
	}
	want := caddyfile.Caddyfile{Box: frontBox{}, Preset: "coolify", Directory: "/data/coolify/proxy/caddy/dynamic",
		Container: "coolify-proxy", Config: "/config/caddy/Caddyfile.autosave", Network: "coolify"}
	if opened != want {
		t.Errorf("openFront() = %+v, want %+v", opened, want)
	}
	if at := destination(opened); at != "/data/coolify/proxy/caddy/dynamic/"+caddyfile.FileName {
		t.Errorf("the rendering goes to %q, want Coolify's dynamic directory", at)
	}
}

func TestTheSwitchboardHearsYourCaddyOnItsNetworkOnTheHTTPSListenerAlone(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, coolifysCaddy())
	if at := slices.Index(board.command, "--https-listen"); at < 0 || board.command[at+1] != "coolify:"+switchboard.HTTPSListenPort {
		t.Errorf("the switchboard serves as %q, want --https-listen coolify:%s: your Caddy reaches it by name there", board.command, switchboard.HTTPSListenPort)
	}
	if slices.Contains(board.command, "--relay-network") {
		t.Errorf("the switchboard serves as %q, want nothing relayed: every tenant on coolify would choose its X-Forwarded-Host", board.command)
	}
	if len(board.ports) != 0 {
		t.Errorf("the switchboard publishes %v, want nothing published beside a network", board.ports)
	}
	if !slices.ContainsFunc(board.networks, func(joined userNetwork) bool { return joined.name == "coolify" }) {
		t.Errorf("the switchboard joins %v at docker run, want coolify: the listener resolves its address as serve starts", board.networks)
	}
	if !slices.Contains(board.env, switchboard.PlaceEnv+"=/data/coolify/proxy/caddy/dynamic") {
		t.Errorf("the switchboard runs with %q, want it placing in Coolify's dynamic directory", board.env)
	}
}

func TestTheSwitchboardIsPublishedOnLoopbackForACaddyOnTheHost(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, caddyService())
	running := strings.Join(board.run(), " ")
	if !strings.Contains(running, "--publish 127.0.0.1:8480:"+switchboard.HTTPSListenPort) {
		t.Errorf("the switchboard runs as %q, want 127.0.0.1:8480 published onto its https listener", running)
	}
	if at := slices.Index(board.command, "--https-listen"); at < 0 || board.command[at+1] != ProxyNetwork+":"+switchboard.HTTPSListenPort {
		t.Errorf("the switchboard serves as %q, want its https listener on its %s address, which the publish forwards to", board.command, ProxyNetwork)
	}
}

func TestACaddyServiceIsReloadedThroughOneSudoersLineThatBootstrapWritesAndGrants(t *testing.T) {
	t.Parallel()

	items := ProxyItems(ArchAMD64, caddyService())
	line := itemAt(t, items, KindFile, sudoersCaddyReload)
	if want := deployUser + " ALL=(root) NOPASSWD: /usr/bin/systemctl reload caddy.service\n"; string(line.Content) != want || line.Mode != 0o440 || line.Owner != rootOwner {
		t.Errorf("%s holds %q at %04o owned by %s, want %q at 0440 owned by root", sudoersCaddyReload, line.Content, line.Mode, line.Owner, want)
	}
	if slices.ContainsFunc(ProxyItems(ArchAMD64, coolifysCaddy()), func(item Item) bool { return item.Name == sudoersCaddyReload }) {
		t.Errorf("a Caddy in a container is given %s, want no sudo: docker exec reloads it", sudoersCaddyReload)
	}
	granted := slices.ContainsFunc(grants(environment.TierProduction, ArchAMD64), func(grant Grant) bool {
		return strings.Contains(grant.Name, sudoersCaddyReload) && strings.Contains(grant.Detail, strings.TrimSpace(string(line.Content)))
	})
	if !granted {
		t.Errorf("the grants never name %s and the line it holds", sudoersCaddyReload)
	}
}

func TestDestroyUnplacesOcelCaddyReloadsYourCaddyAndTakesTheSudoersLine(t *testing.T) {
	t.Parallel()

	read := Reading{Tier: environment.TierProduction, Front: caddyService(), Observed: map[string]string{
		KindFile + " " + sudoersCaddyReload:        "",
		KindContainer + " " + SwitchboardContainer: "",
	}}
	taken := removing(read, Reading{Tier: environment.TierPreview}, appsPresent{})
	at := slices.IndexFunc(taken, func(r removal) bool { return r.kind == KindPlaced })
	if at < 0 {
		t.Fatalf("the last tier's destroy takes %v, want /etc/caddy/ocel.d/ocel.caddy unplaced", taken)
	}
	command := taken[at].command()
	for _, want := range []string{"'unplace' '/etc/caddy/ocel.d/ocel.caddy'", "rm -f '/etc/caddy/ocel.d/ocel.caddy'", "'reload' 'caddy.service'"} {
		if !strings.Contains(command, want) {
			t.Errorf("the destroy runs\n%s\nwant %s in it: your Caddy otherwise keeps routing ocel's hostnames to a switchboard that is gone", command, want)
		}
	}
	board := slices.IndexFunc(taken, func(r removal) bool { return r.kind == KindContainer && r.path == SwitchboardContainer })
	if board < at {
		t.Errorf("the destroy removes the switchboard before it unplaces %s, want it unplaced through the switchboard first", caddyfile.FileName)
	}
	if !slices.ContainsFunc(taken, func(r removal) bool { return r.kind == KindFile && r.path == sudoersCaddyReload }) {
		t.Errorf("the destroy takes %v, want %s taken with it", taken, sudoersCaddyReload)
	}
}

func TestBootstrapRefusesACaddyWhoseAdminEndpointIsOff(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "/config/apps/http/servers") {
			return session.Result{Code: 7, Stderr: "curl: (7) Failed to connect to 127.0.0.1 port 2019"}, true
		}
		return session.Result{}, false
	}
	err := box.fronted(caddyService()).servingFree(context.Background(), Reading{Tier: environment.TierProduction, Front: caddyService()})
	if err == nil || !strings.Contains(err.Error(), "admin off") {
		t.Errorf("servingFree() = %v, want a Caddy with its admin endpoint off refused: caddy reload needs it", err)
	}
	if slices.ContainsFunc(box.commands(), func(command string) bool { return strings.Contains(command, "docker") }) {
		t.Errorf("reading a Caddy that runs as a service ran %q, want no docker asked: the box may have no engine yet", box.commands())
	}
}

func TestBootstrapRefusesACaddyContainerOffTheHostsNetworkWithoutANetworkNamingIt(t *testing.T) {
	t.Parallel()

	filled := CaddyFront{Directory: "/etc/caddy/ocel.d", Container: "caddy"}.Filled()
	front := Front{Caddy: &filled}
	box := machine(nil)
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "NetworkMode") {
			return session.Result{Stdout: "bridge\n"}, true
		}
		return session.Result{}, false
	}
	err := box.fronted(front).servingFree(context.Background(), Reading{Tier: environment.TierProduction, Front: front})
	if err == nil || !strings.Contains(err.Error(), "proxy.caddy.network") {
		t.Errorf("servingFree() = %v, want it refused naming proxy.caddy.network", err)
	}
}

func TestADeployStandingTheSwitchboardAgainTakesCoolifysCaddyDirectoryItCannotEnterAsThere(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root enters every directory, and this is about the deploy login, which cannot")
	}
	root := t.TempDir()
	shut := filepath.Join(root, "coolify", "proxy")
	for _, dir := range []string{filepath.Join(shut, "caddy", "dynamic"), filepath.Join(root, "etc", "caddy")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(shut, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(shut), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(shut), 0o755) })

	for dir, missing := range map[string]bool{
		filepath.Join(shut, "caddy", "dynamic"):  false,
		filepath.Join(root, "etc", "caddy", "d"): true,
	} {
		board := boundToPlace(boxContainer{}, filepath.Join(dir, caddyfile.FileName))
		said, err := exec.Command("sh", "-c", presenceRead(board)).CombinedOutput()
		if err != nil {
			t.Fatalf("the presence read failed: %v\n%s", err, said)
		}
		if reported := strings.Contains(string(said), "missing="+dir); reported != missing {
			t.Errorf("the presence read of %s says %q, want it missing = %v: Coolify's directory sits under /data/coolify/proxy, 0700, which the deploy login cannot enter", dir, said, missing)
		}
		stood, err := exec.Command("sh", "-c", board.placePresent()).CombinedOutput()
		if refused := err != nil; refused != missing {
			t.Errorf("standing the switchboard onto %s = %v, %q; want it refused = %v", dir, err, stood, missing)
		}
	}
	if restoring := switchboardBox(nil, coolifysCaddy()).restoring(1); !strings.Contains(restoring, "/data/coolify/proxy/caddy/dynamic") {
		t.Errorf("a deploy stands the switchboard again as\n%s\nwithout checking the directory it places in", restoring)
	}
}
