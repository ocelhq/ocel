package dockerpolicy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pathShapes(granted, state string) []string {
	return []string{
		".", "..", "./", "../", "./etc", "../etc", ".hidden", "~", "~/", "~root", "~/x", "etc/x", "a/b", "/", "//", "/etc", "//etc",
		"/var/../etc", "/./etc", granted + "/..", granted + "/.", granted + "/", granted + "//", granted + "/../granted",
		state + "/..", "/proc/self/root", "/dev", "/var/run/docker.sock", "C:\\", "\\\\host\\share",
	}
}

func TestNoBindOrVolumeSourceShapeReachesAPathBootstrapDidNotGrant(t *testing.T) {
	t.Parallel()

	policy, granted := aPolicy(t)
	for _, source := range pathShapes(granted, policy.StateRoot) {
		for _, argv := range [][]string{
			{"run", "--network", "ocel", "--volume", source + ":/h", "alpine"},
			{"run", "--network", "ocel", "--volume", source + ":/h:ro", "alpine"},
			{"run", "--network", "ocel", "--volume=" + source + ":/h", "alpine"},
			{"run", "--network", "ocel", "--mount", "type=bind,src=" + source + ",dst=/h,readonly", "alpine"},
			{"run", "--network", "ocel", "--mount", "type=volume,src=" + source + ",dst=/h", "alpine"},
			{"run", "--network", "ocel", "--mount", "src=" + source + ",dst=/h", "alpine"},
			{"create", "--network", "ocel", "--volume", source + ":/h", "alpine"},
		} {
			inv, err := policy.Open(context.Background(), argv)
			inv.Close()
			if err == nil {
				t.Errorf("docker %s was let through: %q is not a path bootstrap granted, and the daemon resolves it on the box", strings.Join(argv, " "), source)
			}
		}
	}
}

func TestAVolumeNamedLikeAVolumeIsLookedUpAndAGrantedBindIsLetThrough(t *testing.T) {
	t.Parallel()

	policy, granted := aPolicy(t)
	for _, argv := range [][]string{
		{"run", "--network", "ocel", "--volume", "shop-cache-kv:/data", "alpine"},
		{"run", "--network", "ocel", "--volume", "shop.cache_kv-1:/data", "alpine"},
		{"run", "--network", "ocel", "--mount", "type=volume,src=shop-cache-kv,dst=/data", "alpine"},
		{"run", "--network", "ocel", "--volume", granted + ":/g:ro", "alpine"},
		{"run", "--network", "ocel", "--mount", "type=bind,src=" + granted + ",dst=/g,readonly", "alpine"},
		{"run", "--network", "ocel", "--volume", policy.StateRoot + ":/s", "alpine"},
	} {
		inv, err := policy.Open(context.Background(), argv)
		inv.Close()
		if err != nil {
			t.Errorf("docker %s was refused: %v", strings.Join(argv, " "), err)
		}
	}
}

func TestOnlyANetworkFlagBeforeTheImageNamesTheContainersNetwork(t *testing.T) {
	t.Parallel()

	policy, _ := aPolicy(t)
	flags := [][]string{{"--network", "ocel"}, {"--network=ocel"}}
	for _, verb := range []string{"run", "create"} {
		for _, network := range flags {
			for _, argv := range [][]string{
				append(append([]string{verb, "alpine"}, network...), "sleep"),
				append([]string{verb, "-d", "alpine", "sleep"}, network...),
				append(append([]string{verb, "--rm", "alpine", "--"}, network...), "x"),
				append([]string{verb, "--", "alpine"}, network...),
			} {
				inv, err := policy.Open(context.Background(), argv)
				inv.Close()
				if err == nil {
					t.Errorf("docker %s was let through: its only --network is an argument to the image, so the container lands on the default bridge", strings.Join(argv, " "))
				}
			}
			argv := append(append([]string{verb, "--rm"}, network...), "alpine", "--network", "host")
			inv, err := policy.Open(context.Background(), argv)
			inv.Close()
			if err != nil {
				t.Errorf("docker %s was refused, and its image arguments are its own: %v", strings.Join(argv, " "), err)
			}
		}
	}
}

func TestEveryNetworkVerbOnAContainerNeedsTheContainerToBeOcels(t *testing.T) {
	t.Parallel()

	policy, _ := aPolicy(t)
	for _, argv := range [][]string{
		{"network", "connect", "ocel-production-shop", "coolify-db"},
		{"network", "connect", "--alias", "db", "ocel-production-shop", "coolify-db"},
		{"network", "disconnect", "ocel-production-shop", "coolify-db"},
		{"network", "disconnect", "--force", "coolify", "coolify-proxy"},
		{"network", "disconnect", "ocel-production-shop"},
		{"network", "disconnect", "--privileged", "ocel-production-shop", "shop-web-abc"},
	} {
		inv, err := policy.Open(context.Background(), argv)
		inv.Close()
		if err == nil {
			t.Errorf("docker %s was let through: the container is not one ocel runs", strings.Join(argv, " "))
		}
	}
}

func TestASymbolicLinkGrantedAsABindIsRefused(t *testing.T) {
	t.Parallel()

	policy, _ := aPolicy(t)
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink("/", linked); err != nil {
		t.Fatal(err)
	}
	policy.Binds = append(policy.Binds, policy.Binds[0])
	policy.Binds[len(policy.Binds)-1].Path = linked
	inv, err := policy.Open(context.Background(), []string{"run", "--network", "ocel", "--volume", linked + ":/h:ro", "alpine"})
	inv.Close()
	if err == nil {
		t.Errorf("a granted path that is a symbolic link to / was mounted")
	}
}
