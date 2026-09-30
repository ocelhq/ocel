package host

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

func handedTo(spec Container) handoff {
	delivery, err := handing(spec)
	if err != nil {
		panic(err)
	}
	return delivery
}

const (
	appContainer      = "the app container a deploy runs"
	proxyContainer    = "the proxy container a bootstrap runs"
	boardContainer    = "the switchboard container a bootstrap runs"
	resourceContainer = "the resource container a deploy runs"
	tunnelContainer   = "the tunnel container a claim through a tunnel runs"
	tokenPlacer       = "the container that places the tunnel's token"
)

func resourced() ResourceContainer {
	return ResourceContainer{
		Name: "prod-web-r0a1b2c3d-main-pg", Project: valued().Project, Resource: "main", Tier: valued().Tier,
		Image:        "public.ecr.aws/docker/library/postgres:17.6@sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929",
		Capabilities: []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID"},
		Volume:       Volume{Path: "/var/lib/postgresql/data"},
		Credential:   Credential{Env: "POSTGRES_PASSWORD"},
	}
}

func running() map[string]string {
	return map[string]string{
		appContainer:      words(containerRun(valued(), handedTo(valued()))),
		proxyContainer:    words(frontProxy().run()),
		boardContainer:    words(switchboardBox(nil, Front{}).run()),
		resourceContainer: words(resourceRun(resourced(), "0123456789ab", EnvFile(resourced().Tier, resourced().Name))),
		tunnelContainer:   words(tunnelBox(Tunnel{ID: "5a6b7c8d-1"}).run()),
		tokenPlacer:       words(tunnelTokenPlacing("place-secret")),
	}
}

func entitledPaths() map[string][]string {
	return map[string][]string{
		appContainer:      {EnvFile(valued().Tier, valued().Name)},
		proxyContainer:    {proxyRoot, caddy.PinsDir, ProxyData},
		boardContainer:    {live.RoutingDir, live.RoutingTable},
		resourceContainer: {EnvFile(resourced().Tier, resourced().Name)},
		tunnelContainer:   {TunnelDir},
		tokenPlacer:       {TunnelDir},
	}
}

func TestNoContainerIsHandedTheSocketThatIsRootUnderAnotherName(t *testing.T) {
	t.Parallel()

	for what, command := range running() {
		if strings.Contains(command, "docker.sock") {
			t.Errorf("%s runs %q: the daemon socket is root on this machine, and a container with it mounted opens every sealed value and reads every sibling's environment",
				what, command)
		}
	}
}

func TestNoContainerSharesAProcessNamespaceWithAnother(t *testing.T) {
	t.Parallel()

	for what, command := range running() {
		for _, shared := range []string{"--pid=host", "--pid=container:", quoted("--pid")} {
			if strings.Contains(command, shared) {
				t.Errorf("%s runs %q, and %q makes a sibling's /proc/1/environ readable from inside it",
					what, command, shared)
			}
		}
	}
}

func TestNoContainerIsRunPrivileged(t *testing.T) {
	t.Parallel()

	for what, command := range running() {
		if strings.Contains(command, "--privileged") {
			t.Errorf("%s runs %q, and a privileged container is the host", what, command)
		}
	}
}

func source(token string) string {
	bare := strings.Trim(token, "'")
	if from, _, cut := strings.Cut(bare, ":"); cut {
		return from
	}
	return bare
}

func underARoot(path string) bool {
	for _, root := range []string{tierRoot, stateRoot} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func includes(paths []string, path string) bool {
	for _, one := range paths {
		if one == path {
			return true
		}
	}
	return false
}

func TestEveryPathAContainerIsToldAboutUnderTheKeyOrTheRecordsIsOneItIsEntitledTo(t *testing.T) {
	t.Parallel()

	allowed := entitledPaths()
	for what, command := range running() {
		if len(allowed[what]) == 0 {
			t.Fatalf("%s is rendered by this bench and nothing says which paths under %s and %s it is entitled to, so what it names proves nothing",
				what, tierRoot, stateRoot)
		}
		named := 0
		for _, token := range strings.Fields(command) {
			path := source(token)
			if !underARoot(path) {
				continue
			}
			named++
			if !includes(allowed[what], path) {
				t.Errorf("%s runs %q and names %q, which is none of %v: the seal key, every sealed record and every other app's values live under %s and %s, and a run has business with nothing there but what it is entitled to",
					what, command, path, allowed[what], tierRoot, stateRoot)
			}
		}
		if named == 0 {
			t.Errorf("%s names no path under %s or %s at all, so this walk reads no token of it and would say the same of a run that mounted every one of them",
				what, tierRoot, stateRoot)
		}
	}
}

func TestNothingAContainerIsEntitledToIsTheKeyTheRecordsOrTheTierStateItself(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for what, allowed := range entitledPaths() {
			for _, path := range allowed {
				for _, refused := range []string{TierDir(tier), KeyValuesDir(tier), SealKeyPath(tier)} {
					if path == refused || strings.HasPrefix(path, refused+"/") {
						t.Errorf("%s is entitled to %q, which sits under %s: the key that opens every sealed value and the records it sealed are what that path contains",
							what, path, refused)
					}
				}
			}
		}
	}
}

func TestEveryContainerThisPackageRunsIsBoundByTheIsolationRules(t *testing.T) {
	t.Parallel()

	rendered(t, `[]string{"docker", "run"`, []string{"containerRun", "placementRun", "resourceRun", "run", "tunnelTokenPlacing"},
		"a container run built somewhere this bench does not read is bound by none of the rules in this file")
}

func TestAPlacementContainerIsBoundByTheIsolationRulesAndMountsNothingButItsDirectoryAndTheSwitchboardBinary(t *testing.T) {
	t.Parallel()

	command := words(placementFed("/etc/traefik/dynamic", "place", "/etc/traefik/dynamic/ocel.yml"))
	for _, refused := range []string{"docker.sock", quoted("--pid"), "--pid=", "--privileged", "--volume"} {
		if strings.Contains(command, refused) {
			t.Errorf("a placement runs %q, and names %s", command, refused)
		}
	}
	var mounted []string
	for _, token := range strings.Fields(command) {
		for _, field := range strings.Split(strings.Trim(token, "'"), ",") {
			if from, cut := strings.CutPrefix(field, "source="); cut {
				mounted = append(mounted, from)
			}
		}
	}
	if want := []string{SwitchboardDir, "/etc/traefik/dynamic"}; !slices.Equal(mounted, want) {
		t.Errorf("a placement runs %q and mounts %q, want %q alone: nothing under %s or %s is its business", command, mounted, want, tierRoot, stateRoot)
	}
}
