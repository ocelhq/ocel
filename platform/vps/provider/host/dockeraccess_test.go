package host

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/dockerpolicy"
	"github.com/ocelhq/ocel/platform/vps/provider/envfile"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/resources/container"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
	"github.com/ocelhq/ocel/platform/vps/provider/tunnel"
)

type everyContainerOcels struct{}

func (everyContainerOcels) IsOcels(context.Context, string) (bool, error) { return true, nil }

func (everyContainerOcels) IsHostBound(context.Context, string) (bool, error) { return false, nil }

func composedArgvs(fronting front.Proxy) map[string][]string {
	tunneled := tunnel.Service(boxapi.Tunnel{Edge: "cloudflare", ID: "5a6b7c8d-1"}, newConnector())
	board := switchboardBox(nil, fronting)
	gateway := resourced()
	gateway.Mounts = []container.Mount{{Source: RealtimeDir, Target: RealtimeMount}}
	argvs := map[string][]string{
		"an app":                       release.ContainerRun(valued(), handedTo(valued())),
		"a resource":                   container.Run(resourced(), "0123456789ab", envfile.Path(resourced().Tier, resourced().Name)),
		"the realtime gateway":         container.Run(gateway, "0123456789ab", envfile.Path(gateway.Tier, gateway.Name)),
		"the switchboard, recreated":   board.Create(),
		"the switchboard, run":         board.Run(),
		"the tunnel":                   tunneled.Run(),
		"the tunnel's token":           tunnel.TokenArgv("place-secret", "ocel-203-0-113-10-0a1b2c3d"),
		"the tunnel's readiness":       tunneled.Readiness(),
		"the switchboard's readiness":  board.Readiness(),
		"a resource's readiness":       append([]string{"docker", "exec", resourced().Name}, "pg_isready"),
		"a credential reasserted":      append([]string{"docker", "exec", "--interactive", resourced().Name}, "psql"),
		"the proxy's readiness":        frontProxy().Readiness(),
		"the proxy's mounts, compared": append([]string{"docker", "exec", caddy.Container}, frontProxy().Inodes...),
	}
	if placed := routing.Destination(fronting.Front().Open(frontBox{})); placed != "" {
		argvs["a placement"] = routing.Placement(filepath.Dir(placed), "origin-group")
		argvs["a fed placement"] = routing.PlacementFed(filepath.Dir(placed), "place-origin", placed)
	}
	if fronting.Caddy != nil && fronting.Caddy.Container != "" {
		argvs["the adopted proxy's reload"] = []string{"docker", "exec", fronting.Caddy.Container, "caddy", "reload"}
	}
	return argvs
}

func TestEveryDockerCommandADeployComposesIsOneTheCheckLetsThrough(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]front.Proxy{"ocel's own proxy": {}, "coolify's traefik": coolifysTraefik(), "coolify's caddy": coolifysCaddy(), "a proxy on a network": routedOnANetwork()} {
		mirror := t.TempDir()
		policy := dockerpolicy.Policy{
			DockerGrants: dockerGrants(front),
			StateRoot:    mirror,
			Inspect:      everyContainerOcels{},
			Ancestors:    func(string) error { return nil },
		}
		for _, bind := range policy.Binds {
			if err := os.MkdirAll(bind.Path, 0o755); err != nil && !os.IsPermission(err) {
				t.Fatal(err)
			}
		}
		for what, argv := range composedArgvs(front) {
			mirrored := make([]string, 0, len(argv))
			for _, arg := range argv {
				if strings.HasPrefix(arg, boxapi.StateRoot+"/") && strings.HasSuffix(arg, envfile.Suffix) {
					arg = filepath.Join(mirror, strings.TrimPrefix(arg, boxapi.StateRoot+"/"))
					if err := os.MkdirAll(filepath.Dir(arg), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(arg, nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				mirrored = append(mirrored, arg)
			}
			inv, err := policy.Open(context.Background(), mirrored[1:])
			inv.Close()
			if err != nil && !strings.Contains(err.Error(), "cannot be mounted") {
				t.Errorf("behind %s, %s runs `%s`, which the checked docker refuses: %v", name, what, strings.Join(argv, " "), err)
			}
		}
	}
}

func TestTheDeployLoginIsInNoGroupThatReachesTheDockerSocket(t *testing.T) {
	t.Parallel()

	facts := string(principal().Content)
	if !strings.Contains(facts, "groups=\n") {
		t.Errorf("the deploy login is surveyed as %q, want it in no supplementary group, docker's above all", facts)
	}
	if strings.Contains(deployLogin().command(), "-aG") || strings.Contains(deployLogin().command(), " -G '"+"docker") {
		t.Errorf("the deploy login is written with %q, which joins it to a group", deployLogin().command())
	}
	if !strings.Contains(deployLogin().command(), "-G ''") {
		t.Errorf("an existing deploy login keeps the groups it had, docker's among them: %s", deployLogin().command())
	}
}

func TestTheDeployLoginsDockerIsTheCheckedOneOnEveryPathItRunsCommandsBy(t *testing.T) {
	t.Parallel()

	if !strings.Contains(string(loginShell()), "PATH="+dockerShims+":") {
		t.Errorf("the deploy login's shell sets %q, so the docker it runs is the daemon's own client, which it is no longer allowed to reach", loginShell())
	}
	for _, login := range []string{"root", "ubuntu"} {
		box := newBench(nil)
		box.dest.User = login
		box.facts.Root = login == "root"
		if _, err := box.host().Run(context.Background(), ssh.Deploy, "act as the deploy login", "true", nil); err != nil {
			t.Fatal(err)
		}
		acting := box.commands()[len(box.commands())-1]
		if !strings.Contains(acting, "PATH="+boxapi.DeployPath) {
			t.Errorf("a %s login acts as %s through %q, which leaves the daemon's own client first on its PATH", login, deployUser, acting)
		}
	}
	if !strings.Contains(string(dockerSudoers()), LiveBinary+" "+boxapi.DockerCommand+" *") {
		t.Errorf("the sudo line is %q, want it to reach docker only through the check", dockerSudoers())
	}
}

func TestEveryBindTheCheckedDockerAllowsSitsUnderDirectoriesTheDeployLoginCannotRename(t *testing.T) {
	t.Parallel()

	owned := map[string]bool{}
	for _, item := range Items("production", nil, ssh.AMD64, coolifysTraefik()) {
		if item.Kind == machine.KindDir && item.Owner == deployUser {
			owned[item.Name] = true
		}
	}
	for _, bind := range dockerGrants(coolifysTraefik()).Binds {
		for parent := filepath.Dir(bind.Path); parent != "/"; parent = filepath.Dir(parent) {
			if owned[parent] {
				t.Errorf("%s sits under %s, which bootstrap hands %s: it can rename it and put a link to / in its place before a container mounts it", bind.Path, parent, deployUser)
			}
		}
	}
	if !slices.ContainsFunc(Items("production", nil, ssh.AMD64, front.Proxy{}), func(item machine.Item) bool {
		return item.Kind == machine.KindDir && item.Name == boxapi.StateRoot && item.Owner == machine.RootOwner
	}) {
		t.Errorf("%s is not root's, and every bind beneath it is one the deploy login can swap", boxapi.StateRoot)
	}
}
