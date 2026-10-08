package readiness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
)

const tokenVariable = "REGISTRY_TOKEN"

func unsetTokenVariable(t *testing.T) {
	t.Helper()
	t.Setenv(tokenVariable, "")
	if err := os.Unsetenv(tokenVariable); err != nil {
		t.Fatal(err)
	}
}

func registryConfig(registry *project.Registry) *project.Project {
	return &project.Project{
		Path:     "/repo/ocel.config.ts",
		Slug:     "shop",
		Apps:     []project.App{{Name: "web", Path: "services/web", Compute: "container"}},
		Registry: registry,
	}
}

func TestAProjectRegistryRidesTheDeployWithItsPasswordReadFromTheEnvironment(t *testing.T) {
	t.Setenv(tokenVariable, "hunter2")

	registry, err := ProjectRegistry(registryConfig(&project.Registry{
		Server: "registry.example.com", Namespace: "acme", Username: "acme-bot", Password: tokenVariable,
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
	t.Setenv(tokenVariable, "hunter2")
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable})
	cfg.Apps = nil

	registry, err := ProjectRegistry(cfg)
	if err != nil || registry != nil {
		t.Errorf("ProjectRegistry() = %q, %v, want nothing for a deploy that pushes no image", registry.GetServer(), err)
	}
}

func TestARegistryWhoseVariableIsUnsetIsRefusedBeforeAnythingIsBuilt(t *testing.T) {
	unsetTokenVariable(t)
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable})

	_, err := ProjectRegistry(cfg)
	if err == nil {
		t.Fatal("ProjectRegistry() passed with the registry's variable unset, so the deploy would build before discovering it cannot push")
	}
	for _, want := range []string{tokenVariable, "registry.example.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ProjectRegistry() error = %v, want it to mention %q", err, want)
		}
	}
}

func TestARegistryWhoseVariableIsEmptyIsRefusedToo(t *testing.T) {
	t.Setenv(tokenVariable, "")
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable})

	if _, err := ProjectRegistry(cfg); err == nil {
		t.Fatal("ProjectRegistry() passed with the registry's variable empty, which authenticates as nobody")
	}
}

func TestAProjectWithNoAppIsAskedForNoRegistryPassword(t *testing.T) {
	unsetTokenVariable(t)
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable})
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
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable})

	t.Setenv(tokenVariable, "the-one-checked-at-preflight")
	if _, err := ProjectRegistry(cfg); err != nil {
		t.Fatalf("ProjectRegistry() = %v", err)
	}

	t.Setenv(tokenVariable, "the-one-the-push-uses")
	registry, err := ProjectRegistry(cfg)
	if err != nil {
		t.Fatalf("ProjectRegistry() error = %v", err)
	}
	if registry.GetPassword() != "the-one-the-push-uses" {
		t.Error("ProjectRegistry() sent the password read at preflight, want the value the variable has when the request is built")
	}
}

func TestTheRegistryPasswordIsReadFromTheProjectsDotenvWhenTheShellLacksIt(t *testing.T) {
	unsetTokenVariable(t)
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable})
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
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable})
	cfg.Dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Dir, ".env"), []byte("REGISTRY_TOKEN=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tokenVariable, "from-shell")

	registry, err := ProjectRegistry(cfg)
	if err != nil {
		t.Fatalf("ProjectRegistry() error = %v", err)
	}
	if registry.GetPassword() != "from-shell" {
		t.Error("ProjectRegistry() sent the .env's password, want the shell's: the shell wins, as it does for every ${} in the config")
	}
}

func TestARemovalSendsTheRegistryEvenWhenTheConfigNamesNoAppAnyMore(t *testing.T) {
	t.Setenv(tokenVariable, "hunter2")
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Namespace: "acme", Username: "acme-bot", Password: tokenVariable})
	cfg.Apps = nil

	registry, left := RemovalRegistry(cfg)
	if left != "" {
		t.Fatalf("RemovalRegistry() left %q", left)
	}
	if registry.GetServer() != "registry.example.com" || registry.GetPassword() != "hunter2" {
		t.Errorf("RemovalRegistry() = %q with password %q, want the project's registry: the images it holds outlive the apps that pushed them", registry.GetServer(), registry.GetPassword())
	}
}

func TestARemovalOfAProjectNamingNoRegistrySendsNone(t *testing.T) {
	registry, left := RemovalRegistry(registryConfig(nil))
	if registry != nil || left != "" {
		t.Errorf("RemovalRegistry() = %q, %q, want nothing", registry.GetServer(), left)
	}
}

func TestARemovalWhoseDotenvCannotBeReadNamesThatFailureRatherThanAnUnsetVariable(t *testing.T) {
	cfg := registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable})
	cfg.Dir = t.TempDir()
	if err := os.Mkdir(filepath.Join(cfg.Dir, ".env"), 0o700); err != nil {
		t.Fatal(err)
	}

	registry, left := RemovalRegistry(cfg)
	if registry != nil {
		t.Errorf("RemovalRegistry() = %q, want none", registry.GetServer())
	}
	if !strings.Contains(left, "read .env") || strings.Contains(left, "unset") {
		t.Errorf("RemovalRegistry() left %q, want it to name the unreadable .env, not an unset variable", left)
	}
}

func TestARemovalWhoseRegistryVariableIsUnsetSaysToRestoreItBeforeRemovingNotToDropTheRegistry(t *testing.T) {
	unsetTokenVariable(t)
	_, left := RemovalRegistry(registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable}))
	if !strings.Contains(left, "before removing") {
		t.Errorf("RemovalRegistry() left %q, want it to say to restore the variable before removing: the removal is what deletes the images", left)
	}
	for _, deployAdvice := range []string{"before deploying", "drop `registry`"} {
		if strings.Contains(left, deployAdvice) {
			t.Errorf("RemovalRegistry() left %q, want no deploy advice %q: without `registry` a removal cannot find the images", left, deployAdvice)
		}
	}
}

func TestARemovalWhoseRegistryVariableIsUnsetSendsNoneAndNamesWhatStays(t *testing.T) {
	unsetTokenVariable(t)
	registry, left := RemovalRegistry(registryConfig(&project.Registry{Server: "registry.example.com", Password: tokenVariable}))
	if registry != nil {
		t.Errorf("RemovalRegistry() = %q, want none: a token that is gone must not keep a project from being removed", registry.GetServer())
	}
	for _, want := range []string{tokenVariable, "registry.example.com"} {
		if !strings.Contains(left, want) {
			t.Errorf("RemovalRegistry() left %q, want it to name %q", left, want)
		}
	}
}
