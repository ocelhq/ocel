package readiness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
)

func registryConfig(registry *project.Registry) *project.Project {
	return &project.Project{
		Path:     "/repo/ocel.config.ts",
		Slug:     "shop",
		Apps:     []project.App{{Name: "web", Path: "services/web", Compute: "container"}},
		Registry: registry,
	}
}

func TestAProjectRegistryRidesTheDeployWithItsPasswordReadFromTheEnvironment(t *testing.T) {
	t.Setenv("REGISTRY_TOKEN", "hunter2")

	registry, err := ProjectRegistry(registryConfig(&project.Registry{
		Server: "registry.example.com", Namespace: "acme", Username: "acme-bot", Password: "REGISTRY_TOKEN",
	}))
	if err != nil {
		t.Fatalf("ProjectRegistry() error = %v", err)
	}
	if registry.GetServer() != "registry.example.com" || registry.GetNamespace() != "acme" || registry.GetUsername() != "acme-bot" || registry.GetPassword() != "hunter2" {
		t.Errorf("ProjectRegistry() = server %q namespace %q username %q, want the project's own registry with its password read from the environment",
			registry.GetServer(), registry.GetNamespace(), registry.GetUsername())
	}
}

func TestAProjectNamingNoRegistrySendsNone(t *testing.T) {
	registry, err := ProjectRegistry(registryConfig(nil))
	if err != nil {
		t.Fatalf("ProjectRegistry() error = %v", err)
	}
	if registry != nil {
		t.Errorf("ProjectRegistry() = %q, want nothing: the provider resolves its own registry inside the deploy, and its credentials never reach the CLI",
			registry.GetServer())
	}
}

func TestAProjectWithNoAppSendsNoRegistry(t *testing.T) {
	t.Setenv("REGISTRY_TOKEN", "hunter2")
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: "REGISTRY_TOKEN"})
	cfg.Apps = nil

	registry, err := ProjectRegistry(cfg)
	if err != nil || registry != nil {
		t.Errorf("ProjectRegistry() = %q, %v, want nothing for a deploy that pushes no image", registry.GetServer(), err)
	}
}

func TestARegistryWhoseVariableIsUnsetIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: "REGISTRY_TOKEN"})

	_, err := ProjectRegistry(cfg)
	if err == nil {
		t.Fatal("ProjectRegistry() passed with the registry's variable unset, so the deploy would build before discovering it cannot push")
	}
	for _, want := range []string{"REGISTRY_TOKEN", "registry.example.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ProjectRegistry() error = %v, want it to mention %q", err, want)
		}
	}
}

func TestARegistryWhoseVariableIsEmptyIsRefusedToo(t *testing.T) {
	t.Setenv("REGISTRY_TOKEN", "")
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: "REGISTRY_TOKEN"})

	if _, err := ProjectRegistry(cfg); err == nil {
		t.Fatal("ProjectRegistry() passed with the registry's variable empty, which authenticates as nobody")
	}
}

func TestAProjectWithNoAppIsAskedForNoRegistryPassword(t *testing.T) {
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: "REGISTRY_TOKEN"})
	cfg.Apps = nil

	if _, err := ProjectRegistry(cfg); err != nil {
		t.Errorf("ProjectRegistry() = %v, want a deploy that pushes no image to demand no secret", err)
	}
}

func TestAProjectWithNoRegistryIsAskedForNoPassword(t *testing.T) {
	if _, err := ProjectRegistry(registryConfig(nil)); err != nil {
		t.Errorf("ProjectRegistry() = %v, want a project that names no registry to demand nothing", err)
	}
}

func TestTheRegistryPasswordIsReadWhenTheRequestIsBuiltAndNowhereEarlier(t *testing.T) {
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: "REGISTRY_TOKEN"})

	t.Setenv("REGISTRY_TOKEN", "the-one-checked-at-preflight")
	if _, err := ProjectRegistry(cfg); err != nil {
		t.Fatalf("ProjectRegistry() = %v", err)
	}

	t.Setenv("REGISTRY_TOKEN", "the-one-the-push-uses")
	registry, err := ProjectRegistry(cfg)
	if err != nil {
		t.Fatalf("ProjectRegistry() error = %v", err)
	}
	if registry.GetPassword() != "the-one-the-push-uses" {
		t.Error("ProjectRegistry() sent the password read at preflight, want the value the variable has when the request is built")
	}
}

func TestTheRegistryPasswordIsReadFromTheProjectsDotenvWhenTheShellLacksIt(t *testing.T) {
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: "REGISTRY_TOKEN"})
	cfg.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Dir, ".env"), []byte("REGISTRY_TOKEN=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ProjectRegistry(cfg); err != nil {
		t.Fatalf("ProjectRegistry() = %v, want a placeholder the project's .env fills to pass, as every other ${} in the config does", err)
	}
	registry, err := ProjectRegistry(cfg)
	if err != nil {
		t.Fatalf("ProjectRegistry() error = %v", err)
	}
	if registry.GetPassword() != "from-dotenv" {
		t.Errorf("ProjectRegistry() sent a password of %d bytes, want the value the project's .env sets", len(registry.GetPassword()))
	}
}

func TestTheShellsRegistryPasswordWinsOverTheProjectsDotenv(t *testing.T) {
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: "REGISTRY_TOKEN"})
	cfg.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Dir, ".env"), []byte("REGISTRY_TOKEN=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REGISTRY_TOKEN", "from-shell")

	registry, err := ProjectRegistry(cfg)
	if err != nil {
		t.Fatalf("ProjectRegistry() error = %v", err)
	}
	if registry.GetPassword() != "from-shell" {
		t.Error("ProjectRegistry() sent the .env's password, want the shell's: the shell wins, as it does for every ${} in the config")
	}
}
