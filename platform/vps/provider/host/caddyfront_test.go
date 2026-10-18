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
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func coolifysCaddy() front.Proxy {
	filled := front.Caddy{Preset: "coolify"}.Filled()
	return front.Proxy{Caddy: &filled}
}

func caddyService() front.Proxy {
	filled := front.Caddy{Directory: "/etc/caddy/ocel.d"}.Filled()
	return front.Proxy{Caddy: &filled}
}

func TestACaddyOptionOpensTheCaddyfileProxyOverTheDirectoryItImports(t *testing.T) {
	t.Parallel()

	opened, ok := coolifysCaddy().Front().Open(frontBox{}).(caddyfile.Caddyfile)
	if !ok {
		t.Fatalf("openFront() = %T, want caddyfile.Caddyfile", coolifysCaddy().Front().Open(frontBox{}))
	}
	want := caddyfile.Caddyfile{Box: frontBox{}, Preset: "coolify", Directory: "/data/coolify/proxy/caddy/dynamic", ContainerDirectory: "/dynamic",
		Container: "coolify-proxy", Config: "/config/caddy/Caddyfile.autosave", Network: "coolify"}
	if opened != want {
		t.Errorf("openFront() = %+v, want %+v", opened, want)
	}
	if at := routing.Destination(opened); at != "/data/coolify/proxy/caddy/dynamic/"+caddyfile.FileName {
		t.Errorf("the rendering goes to %q, want Coolify's dynamic directory", at)
	}
}

func TestTheSwitchboardHearsYourCaddyOnItsNetworkOnTheHTTPSListenerAlone(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, coolifysCaddy())
	if at := slices.Index(board.Command, "--https-listen"); at < 0 || board.Command[at+1] != "coolify:"+boxapi.HTTPSListenPort {
		t.Errorf("the switchboard serves as %q, want --https-listen coolify:%s: your Caddy reaches it by name there", board.Command, boxapi.HTTPSListenPort)
	}
	if slices.Contains(board.Command, "--relay-network") {
		t.Errorf("the switchboard serves as %q, want nothing relayed: every tenant on coolify would choose its X-Forwarded-Host", board.Command)
	}
	if len(board.Ports) != 0 {
		t.Errorf("the switchboard publishes %v, want nothing published beside a network", board.Ports)
	}
	if !slices.ContainsFunc(board.Networks, func(joined docker.Network) bool { return joined.Name == "coolify" }) {
		t.Errorf("the switchboard joins %v at docker run, want coolify: the listener resolves its address as serve starts", board.Networks)
	}
}

func TestTheSwitchboardIsPublishedOnLoopbackForACaddyOnTheHost(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, caddyService())
	running := strings.Join(board.Run(), " ")
	if !strings.Contains(running, "--publish 127.0.0.1:8480:"+boxapi.HTTPSListenPort) {
		t.Errorf("the switchboard runs as %q, want 127.0.0.1:8480 published onto its https listener", running)
	}
	if at := slices.Index(board.Command, "--https-listen"); at < 0 || board.Command[at+1] != ProxyNetwork+":"+boxapi.HTTPSListenPort {
		t.Errorf("the switchboard serves as %q, want its https listener on its %s address, which the publish forwards to", board.Command, ProxyNetwork)
	}
}

func TestACaddyServiceIsReloadedThroughOneSudoersLineThatBootstrapWritesAndGrants(t *testing.T) {
	t.Parallel()

	items := ProxyItems(ssh.AMD64, caddyService())
	line := itemAt(t, items, machine.KindFile, machine.SudoersCaddyReload)
	if want := deployUser + " ALL=(root) NOPASSWD: /usr/bin/systemctl reload caddy.service\n"; string(line.Content) != want || line.Mode != 0o440 || line.Owner != machine.RootOwner {
		t.Errorf("%s holds %q at %04o owned by %s, want %q at 0440 owned by root", machine.SudoersCaddyReload, line.Content, line.Mode, line.Owner, want)
	}
	if slices.ContainsFunc(ProxyItems(ssh.AMD64, coolifysCaddy()), func(item machine.Item) bool { return item.Name == machine.SudoersCaddyReload }) {
		t.Errorf("a Caddy in a container is given %s, want no sudo: docker exec reloads it", machine.SudoersCaddyReload)
	}
	granted := slices.ContainsFunc(grants(environment.TierProduction, ssh.AMD64, caddyService()), func(grant Grant) bool {
		return strings.Contains(grant.Name, machine.SudoersCaddyReload) && strings.Contains(grant.Detail, strings.TrimSpace(string(line.Content)))
	})
	if !granted {
		t.Errorf("the grants never name %s and the line it holds", machine.SudoersCaddyReload)
	}
}

func TestTheCaddyReloadLineIsGrantedOnlyWhereBootstrapWritesIt(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]front.Proxy{
		"ocel's own proxy":  {},
		"Coolify's Caddy":   coolifysCaddy(),
		"a manual proxy":    {Manual: &front.Manual{Port: 8480}},
		"a Caddy container": {Caddy: &front.Caddy{Directory: "/etc/caddy/ocel.d", Container: "caddy", Network: "web"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, grant := range grants(environment.TierProduction, ssh.AMD64, front) {
				if strings.Contains(grant.Name, machine.SudoersCaddyReload) {
					t.Errorf("the grants behind %s name %q, want no %s: bootstrap writes it only for a Caddy that runs as caddy.service", name, grant.Name, machine.SudoersCaddyReload)
				}
			}
		})
	}
}

func TestDestroyUnplacesOcelCaddyReloadsYourCaddyAndTakesTheSudoersLine(t *testing.T) {
	t.Parallel()

	read := Reading{Tier: environment.TierProduction, Front: caddyService(), Observed: map[string]string{
		machine.KindFile + " " + machine.SudoersCaddyReload: "",
		machine.KindContainer + " " + boxapi.Switchboard:    "",
	}}
	taken := removing(read, Reading{Tier: environment.TierPreview}, appsPresent{})
	at := slices.IndexFunc(taken, func(r machine.Removal) bool { return r.Kind == machine.KindPlaced })
	if at < 0 {
		t.Fatalf("the last tier's destroy takes %v, want /etc/caddy/ocel.d/ocel.caddy unplaced", taken)
	}
	command := taken[at].Command()
	for _, want := range []string{"'unplace' '/etc/caddy/ocel.d/ocel.caddy'", "rm -f '/etc/caddy/ocel.d/ocel.caddy'", "'reload' 'caddy.service'"} {
		if !strings.Contains(command, want) {
			t.Errorf("the destroy runs\n%s\nwant %s in it: your Caddy otherwise keeps routing ocel's hostnames to a switchboard that is gone", command, want)
		}
	}
	board := slices.IndexFunc(taken, func(r machine.Removal) bool { return r.Kind == machine.KindContainer && r.Path == boxapi.Switchboard })
	if board < at {
		t.Errorf("the destroy removes the switchboard before it unplaces %s, want it unplaced through the switchboard first", caddyfile.FileName)
	}
	if !slices.ContainsFunc(taken, func(r machine.Removal) bool {
		return r.Kind == machine.KindFile && r.Path == machine.SudoersCaddyReload
	}) {
		t.Errorf("the destroy takes %v, want %s taken with it", taken, machine.SudoersCaddyReload)
	}
}

func TestDestroyReloadsYourCaddyOnlyWhenOcelCaddyWasThereToTake(t *testing.T) {
	t.Parallel()

	for name, placed := range map[string]bool{"placed": true, "never placed": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir, bin := t.TempDir(), t.TempDir()
			ran := filepath.Join(bin, "ran")
			docker := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shell.Quote(ran) + "\n[ \"$1\" = inspect ] && echo true\n[ \"$1\" = run ] && exit 1\nexit 0\n"
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(docker), 0o755); err != nil {
				t.Fatal(err)
			}
			if placed {
				if err := os.WriteFile(filepath.Join(dir, caddyfile.FileName), []byte("shop.example.com {\n}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			filled := front.Caddy{Directory: dir, Container: "caddy", Network: "web"}.Filled()
			taken := placedRemovals(front.Proxy{Caddy: &filled})[0]
			run := exec.Command("sh", "-c", taken.Command())
			run.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
			said, err := run.CombinedOutput()
			if err != nil {
				t.Fatalf("the removal failed: %v\n%s", err, said)
			}
			calls, _ := os.ReadFile(ran)
			if reloaded := strings.Contains(string(calls), "caddy reload"); reloaded != placed {
				t.Errorf("with ocel.caddy %s the destroy ran docker %q, want your Caddy reloaded = %v: a destroy that took nothing has nothing for your Caddy to drop", name, calls, placed)
			}
			if placed {
				if _, err := os.Stat(filepath.Join(dir, caddyfile.FileName)); !os.IsNotExist(err) {
					t.Errorf("%s is still there after the destroy: %v", caddyfile.FileName, err)
				}
			}
		})
	}
}

func TestBootstrapRefusesACaddyWhoseAdminEndpointIsOff(t *testing.T) {
	t.Parallel()

	box := newBench(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, "/config/apps/http/servers") {
			return ssh.Result{Code: 7, Stderr: "curl: (7) Failed to connect to 127.0.0.1 port 2019"}, true
		}
		return ssh.Result{}, false
	}
	err := box.fronted(caddyService()).refuseServingPortsHeld(context.Background(), Reading{Tier: environment.TierProduction, Front: caddyService()})
	if err == nil || !strings.Contains(err.Error(), "admin off") {
		t.Errorf("refuseServingPortsHeld() = %v, want a Caddy with its admin endpoint off refused: caddy reload needs it", err)
	}
	if slices.ContainsFunc(box.commands(), func(command string) bool { return strings.Contains(command, "docker") }) {
		t.Errorf("reading a Caddy that runs as a service ran %q, want no docker asked: the box may have no engine yet", box.commands())
	}
}

func TestBootstrapRefusesACaddyContainerOffTheHostsNetworkWithoutANetworkNamingIt(t *testing.T) {
	t.Parallel()

	filled := front.Caddy{Directory: "/etc/caddy/ocel.d", Container: "caddy"}.Filled()
	front := front.Proxy{Caddy: &filled}
	box := newBench(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, "NetworkMode") {
			return ssh.Result{Stdout: "bridge\n"}, true
		}
		return ssh.Result{}, false
	}
	err := box.fronted(front).refuseServingPortsHeld(context.Background(), Reading{Tier: environment.TierProduction, Front: front})
	if err == nil || !strings.Contains(err.Error(), "proxy.caddy.network") {
		t.Errorf("refuseServingPortsHeld() = %v, want it refused naming proxy.caddy.network", err)
	}
}
