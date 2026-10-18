package dockerpolicy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

type ours map[string]bool

func (o ours) IsOcels(_ context.Context, container string) (bool, error) { return o[container], nil }

func (o ours) IsHostBound(_ context.Context, volume string) (bool, error) {
	return volume == "coolify-data", nil
}

func aPolicy(t *testing.T) (Policy, string) {
	t.Helper()
	state := t.TempDir()
	granted := filepath.Join(t.TempDir(), "granted")
	if err := os.Mkdir(granted, 0o755); err != nil {
		t.Fatal(err)
	}
	return Policy{
		DockerGrants: live.DockerGrants{
			Binds:      []live.DockerBind{{Path: granted}, {Path: state, Writable: true}},
			Networks:   []string{"coolify"},
			Containers: []string{"coolify-proxy"},
		},
		StateRoot: state,
		Inspect:   ours{"shop-web-abc": true},
		Ancestors: func(string) error { return nil },
		OwnerUID:  os.Getuid(),
		OwnerGID:  os.Getgid(),
	}, granted
}

func TestEveryWayADockerCommandReachesRootIsRefused(t *testing.T) {
	t.Parallel()

	policy, granted := aPolicy(t)
	outside := filepath.Join(t.TempDir(), "seal.key")
	if err := os.WriteFile(outside, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(policy.StateRoot, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	for what, argv := range map[string][]string{
		"a privileged container":                  {"run", "--privileged", "--network", "ocel", "alpine"},
		"the host's root bound in":                {"run", "--network", "ocel", "--volume", "/:/host", "alpine"},
		"the host's root mounted in":              {"run", "--network", "ocel", "--mount", "type=bind,src=/,dst=/host", "alpine"},
		"the docker socket bound in":              {"run", "--network", "ocel", "--volume", "/var/run/docker.sock:/s", "alpine"},
		"a granted read-only bind made writable":  {"run", "--network", "ocel", "--mount", "type=bind,src=" + granted + ",dst=/g", "alpine"},
		"a capability that escapes":               {"run", "--network", "ocel", "--cap-add", "SYS_ADMIN", "alpine"},
		"the capability that opens by handle":     {"run", "--network", "ocel", "--cap-add", "DAC_READ_SEARCH", "alpine"},
		"the host's network":                      {"run", "--network", "host", "alpine"},
		"another container's network":             {"run", "--network", "container:coolify-proxy", "alpine"},
		"no network named":                        {"run", "alpine"},
		"the host's process namespace":            {"run", "--network", "ocel", "--pid", "host", "alpine"},
		"a device":                                {"run", "--network", "ocel", "--device", "/dev/sda", "alpine"},
		"seccomp turned off":                      {"run", "--network", "ocel", "--security-opt", "seccomp=unconfined", "alpine"},
		"a public port":                           {"run", "--network", "ocel", "--publish", "0.0.0.0:22:22", "alpine"},
		"an env file outside the state root":      {"run", "--network", "ocel", "--env-file", outside, "alpine"},
		"an env file linked out of it":            {"run", "--network", "ocel", "--env-file", escape, "alpine"},
		"a volume bound to a host path":           {"volume", "create", "--opt", "type=none", "--opt", "o=bind", "--opt", "device=/etc", "etc"},
		"a volume mounted with its own options":   {"run", "--network", "ocel", "--mount", "type=volume,src=x,dst=/x,volume-opt=device=/etc", "alpine"},
		"exec into a container ocel does not run": {"exec", "coolify-db", "sh"},
		"exec granted root":                       {"exec", "--privileged", "shop-web-abc", "sh"},
		"another daemon":                          {"-H", "tcp://10.0.0.1:2375", "ps"},
		"a copy out of a container":               {"cp", "shop-web-abc:/etc/shadow", "/tmp"},
		"a build":                                 {"build", "."},
		"a plugin":                                {"plugin", "install", "evil"},
		"an archive written outside":              {"save", "-o", "/etc/cron.d/evil", "alpine"},
		"an archive read from outside":            {"load", "-i", outside},
		"a login whose config the caller picks":   {"login", "--username", "ada", "registry.example.com"},
		"a network the box does not own":          {"network", "connect", "default", "shop-web-abc"},
		"a network on another driver":             {"network", "create", "--driver", "macvlan", "ocel-x"},
		"a foreign container renamed":             {"rename", "coolify-db", "shop-web-abc"},
		"a container renamed to a granted name":   {"rename", "shop-web-abc", "coolify-proxy"},
		"a host-bound volume, mounted":            {"run", "--network", "ocel", "--mount", "type=volume,src=coolify-data,dst=/x", "alpine"},
		"a host-bound volume, named":              {"run", "--network", "ocel", "--volume", "coolify-data:/x", "alpine"},
	} {
		inv, err := policy.Open(context.Background(), argv)
		inv.Close()
		if err == nil {
			t.Errorf("docker %s was let through, %s", strings.Join(argv, " "), what)
		}
	}
}

func TestTheCommandsADeployRunsAreLetThrough(t *testing.T) {
	t.Parallel()

	policy, granted := aPolicy(t)
	env := filepath.Join(policy.StateRoot, "production", "shop-web.env")
	if err := os.MkdirAll(filepath.Dir(env), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"version"},
		{"run", "--detach", "--name", "shop-web-abc", "--restart", "unless-stopped", "--init=false", "--network", "ocel-production-shop",
			"--label", "ocel.tier=production", "--pids-limit", "4096", "--security-opt", "no-new-privileges", "--cap-drop", "ALL",
			"--cap-add", "CHOWN", "--memory", "4294967296", "--env-file", env, "--mount", "type=bind,source=" + granted + ",target=/run/x,readonly",
			"--tmpfs", "/run/ocel:rw", "--env", "PORT=8080", "registry/shop:web", "serve"},
		{"run", "--rm", "--network", "none", "--mount", "type=bind,source=" + policy.StateRoot + ",target=/s", "static", "place"},
		{"run", "--rm", "-i", "--network", "coolify", "--volume", granted + ":/g:ro", "static"},
		{"run", "--detach", "--network", "ocel", "--publish", "127.0.0.1:8480:8480", "static"},
		{"exec", "--interactive", "shop-web-abc", "pg_isready"},
		{"exec", "coolify-proxy", "caddy", "reload"},
		{"network", "create", "--label", "ocel.tier=production", "ocel-production-shop"},
		{"network", "connect", "ocel-production-shop", "ocel-switchboard"},
		{"volume", "create", "--label", "ocel.tier=production", "--driver", "local", "--opt", "type=tmpfs", "shop-cache-kv"},
		{"image", "ls", "-q", "registry/shop:web"},
		{"logs", "--follow", "--timestamps", "shop-web-abc"},
		{"inspect", "--type", "container", "--format", "{{.State.Status}}", "shop-web-abc"},
		{"save", "-o", filepath.Join(policy.StateRoot, "releases", "web.tar"), "registry/shop:web"},
		{"rename", "shop-web-abc", "shop-web-abc-retired"},
		{"run", "--network", "ocel", "--mount", "type=volume,src=shop-cache-kv,dst=/data", "valkey"},
		{"rename", "shop-web-abc", "shop-web-abc-retired"},
	} {
		if err := os.MkdirAll(filepath.Join(policy.StateRoot, "releases"), 0o755); err != nil {
			t.Fatal(err)
		}
		inv, err := policy.Open(context.Background(), argv)
		inv.Close()
		if err != nil {
			t.Errorf("docker %s was refused, and a deploy runs it: %v", strings.Join(argv, " "), err)
		}
	}
}

func TestAnEnvFileReachesDockerAsTheFileTheCallerNamedAndNeverAsAPathItCouldSwap(t *testing.T) {
	t.Parallel()

	policy, _ := aPolicy(t)
	env := filepath.Join(policy.StateRoot, "web.env")
	if err := os.WriteFile(env, []byte("TOKEN=s3cr3t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := policy.Open(context.Background(), []string{"run", "--network", "ocel", "--env-file", env, "alpine"})
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer inv.Close()
	if strings.Contains(strings.Join(inv.Argv, " "), env) || len(inv.Extra) != 1 {
		t.Fatalf("docker is handed %v with %d open files, want the env file as an inherited descriptor in place of its path", inv.Argv, len(inv.Extra))
	}
	read, err := io.ReadAll(inv.Extra[0])
	if err != nil || string(read) != "TOKEN=s3cr3t\n" {
		t.Errorf("the descriptor handed on reads %q, %v", read, err)
	}
}

func TestABindUnderADirectoryTheCallerCanChangeIsRefused(t *testing.T) {
	t.Parallel()

	policy, granted := aPolicy(t)
	policy.Ancestors = nil
	inv, err := policy.Open(context.Background(), []string{"run", "--network", "ocel", "--volume", granted + ":/g:ro", "alpine"})
	inv.Close()
	if err == nil {
		t.Errorf("%s sits under a directory the caller owns, and its bind was let through: whoever can rename that directory swaps what the daemon mounts", granted)
	}
}
