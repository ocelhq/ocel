package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func registryConfig(registry *projectconfig.Registry) *projectconfig.Config {
	return &projectconfig.Config{
		Path:     "/repo/ocel.config.ts",
		Slug:     "shop",
		Apps:     []projectconfig.App{{Name: "web", Path: "services/web", Compute: "container"}},
		Registry: registry,
	}
}

func TestAProjectRegistryRidesTheDeployWithItsPasswordReadFromTheEnvironment(t *testing.T) {
	t.Setenv("GHCR_TOKEN", "hunter2")

	registry, err := projectRegistry(registryConfig(&projectconfig.Registry{
		Server: "ghcr.io", Namespace: "acme", Username: "acme-bot", Password: "GHCR_TOKEN",
	}))
	if err != nil {
		t.Fatalf("projectRegistry() error = %v", err)
	}
	if registry.GetServer() != "ghcr.io" || registry.GetNamespace() != "acme" || registry.GetUsername() != "acme-bot" || registry.GetPassword() != "hunter2" {
		t.Errorf("projectRegistry() = server %q namespace %q username %q, want the project's own registry with its password read from the environment",
			registry.GetServer(), registry.GetNamespace(), registry.GetUsername())
	}
}

func TestAProjectNamingNoRegistrySendsNone(t *testing.T) {
	registry, err := projectRegistry(registryConfig(nil))
	if err != nil {
		t.Fatalf("projectRegistry() error = %v", err)
	}
	if registry != nil {
		t.Errorf("projectRegistry() = %q, want nothing: the provider resolves its own registry inside the deploy, and its credentials never reach the CLI",
			registry.GetServer())
	}
}

func TestAProjectWithNoAppSendsNoRegistry(t *testing.T) {
	t.Setenv("GHCR_TOKEN", "hunter2")
	cfg := registryConfig(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})
	cfg.Apps = nil

	registry, err := projectRegistry(cfg)
	if err != nil || registry != nil {
		t.Errorf("projectRegistry() = %q, %v, want nothing for a deploy that pushes no image", registry.GetServer(), err)
	}
}

func TestARegistryWhoseVariableIsUnsetIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	cfg := registryConfig(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})

	err := requireProjectRegistryPassword(cfg)
	if err == nil {
		t.Fatal("requireProjectRegistryPassword() passed with the registry's variable unset, so the deploy would build before discovering it cannot push")
	}
	for _, want := range []string{"GHCR_TOKEN", "ghcr.io"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("requireProjectRegistryPassword() error = %v, want it to mention %q", err, want)
		}
	}
}

func TestARegistryWhoseVariableIsEmptyIsRefusedToo(t *testing.T) {
	t.Setenv("GHCR_TOKEN", "")
	cfg := registryConfig(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})

	if err := requireProjectRegistryPassword(cfg); err == nil {
		t.Fatal("requireProjectRegistryPassword() passed with the registry's variable empty, which authenticates as nobody")
	}
}

func TestAProjectWithNoAppIsAskedForNoRegistryPassword(t *testing.T) {
	cfg := registryConfig(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})
	cfg.Apps = nil

	if err := requireProjectRegistryPassword(cfg); err != nil {
		t.Errorf("requireProjectRegistryPassword() = %v, want a deploy that pushes no image to demand no secret", err)
	}
}

func TestAProjectWithNoRegistryIsAskedForNoPassword(t *testing.T) {
	if err := requireProjectRegistryPassword(registryConfig(nil)); err != nil {
		t.Errorf("requireProjectRegistryPassword() = %v, want a project that names no registry to demand nothing", err)
	}
}

func TestTheRegistryPasswordIsReadWhenTheRequestIsBuiltAndNowhereEarlier(t *testing.T) {
	cfg := registryConfig(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})

	t.Setenv("GHCR_TOKEN", "the-one-checked-at-preflight")
	if err := requireProjectRegistryPassword(cfg); err != nil {
		t.Fatalf("requireProjectRegistryPassword() = %v", err)
	}

	t.Setenv("GHCR_TOKEN", "the-one-the-push-uses")
	registry, err := projectRegistry(cfg)
	if err != nil {
		t.Fatalf("projectRegistry() error = %v", err)
	}
	if registry.GetPassword() != "the-one-the-push-uses" {
		t.Error("projectRegistry() sent the password read at preflight, want the value the variable has when the request is built")
	}
}

func TestTheRegistryPasswordIsReadFromTheProjectsDotenvWhenTheShellLacksIt(t *testing.T) {
	cfg := registryConfig(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})
	cfg.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Dir, ".env"), []byte("GHCR_TOKEN=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := requireProjectRegistryPassword(cfg); err != nil {
		t.Fatalf("requireProjectRegistryPassword() = %v, want a placeholder the project's .env fills to pass, as every other ${} in the config does", err)
	}
	registry, err := projectRegistry(cfg)
	if err != nil {
		t.Fatalf("projectRegistry() error = %v", err)
	}
	if registry.GetPassword() != "from-dotenv" {
		t.Errorf("projectRegistry() sent a password of %d bytes, want the value the project's .env sets", len(registry.GetPassword()))
	}
}

func TestTheShellsRegistryPasswordWinsOverTheProjectsDotenv(t *testing.T) {
	cfg := registryConfig(&projectconfig.Registry{Server: "ghcr.io", Password: "GHCR_TOKEN"})
	cfg.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Dir, ".env"), []byte("GHCR_TOKEN=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHCR_TOKEN", "from-shell")

	registry, err := projectRegistry(cfg)
	if err != nil {
		t.Fatalf("projectRegistry() error = %v", err)
	}
	if registry.GetPassword() != "from-shell" {
		t.Error("projectRegistry() sent the .env's password, want the shell's: the shell wins, as it does for every ${} in the config")
	}
}
