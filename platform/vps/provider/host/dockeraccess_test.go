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
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

type everyContainerOcels struct{}

func (everyContainerOcels) IsOcels(context.Context, string) (bool, error) { return true, nil }

func (everyContainerOcels) IsHostBound(context.Context, string) (bool, error) { return false, nil }

func composedArgvs(front Front) map[string][]string {
	tunnel := tunnelBox(Tunnel{Edge: "cloudflare", ID: "5a6b7c8d-1"}, newConnector())
	board := switchboardBox(nil, front)
	gateway := resourced()
	gateway.Mounts = []Mount{{Source: RealtimeDir, Target: RealtimeMount}}
	argvs := map[string][]string{
		"an app":                       containerRun(valued(), handedTo(valued())),
		"a resource":                   resourceRun(resourced(), "0123456789ab", EnvFile(resourced().Tier, resourced().Name)),
		"the realtime gateway":         resourceRun(gateway, "0123456789ab", EnvFile(gateway.Tier, gateway.Name)),
		"the switchboard, recreated":   board.create(),
		"the switchboard, run":         board.run(),
		"the tunnel":                   tunnel.run(),
		"the tunnel's token":           renderTunnelTokenArgv("place-secret", "ocel-203-0-113-10-0a1b2c3d"),
		"the tunnel's readiness":       tunnel.readiness(),
		"the switchboard's readiness":  board.readiness(),
		"a resource's readiness":       append([]string{"docker", "exec", resourced().Name}, "pg_isready"),
		"a credential reasserted":      append([]string{"docker", "exec", "--interactive", resourced().Name}, "psql"),
		"the proxy's readiness":        frontProxy().readiness(),
		"the proxy's mounts, compared": append([]string{"docker", "exec", caddy.Container}, frontProxy().inodes...),
	}
	if placed := destination(openFront(front, frontBox{})); placed != "" {
		argvs["a placement"] = placementCommand(filepath.Dir(placed), "origin-group")
		argvs["a fed placement"] = placementFed(filepath.Dir(placed), "place-origin", placed)
	}
	if front.Caddy != nil && front.Caddy.Container != "" {
		argvs["the adopted proxy's reload"] = []string{"docker", "exec", front.Caddy.Container, "caddy", "reload"}
	}
	return argvs
}

func TestEveryDockerCommandADeployComposesIsOneTheCheckLetsThrough(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]Front{"ocel's own proxy": {}, "coolify's traefik": coolifysTraefik(), "coolify's caddy": coolifysCaddy(), "a proxy on a network": routedOnANetwork()} {
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
				if strings.HasPrefix(arg, stateRoot+"/") && strings.HasSuffix(arg, envFileSuffix) {
					arg = filepath.Join(mirror, strings.TrimPrefix(arg, stateRoot+"/"))
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
		box := machine(nil)
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
		if item.Kind == KindDir && item.Owner == deployUser {
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
	if !slices.ContainsFunc(Items("production", nil, ssh.AMD64, Front{}), func(item Item) bool {
		return item.Kind == KindDir && item.Name == stateRoot && item.Owner == rootOwner
	}) {
		t.Errorf("%s is not root's, and every bind beneath it is one the deploy login can swap", stateRoot)
	}
}
