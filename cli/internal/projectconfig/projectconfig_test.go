package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindConfigFile_WalksUpFromNestedSubdirectory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, ConfigFileName)
	if err := os.WriteFile(configPath, []byte("export default {};"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	found, err := findConfigFile(nested)
	if err != nil {
		t.Fatalf("findConfigFile: %v", err)
	}
	if found != configPath {
		t.Fatalf("found = %q, want %q", found, configPath)
	}
}

func TestFindConfigFile_NotFound(t *testing.T) {
	root := t.TempDir()

	_, err := findConfigFile(root)
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func writeConfig(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestResolve_ValidConfig(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  discovery: { paths: ["resources"] },
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.ProjectID != "proj_123" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "proj_123")
	}
	if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "resources" {
		t.Fatalf("Discovery.Paths = %v, want [resources]", cfg.Discovery.Paths)
	}
}

func TestResolve_ParsesProductionDomain(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  domains: { production: "App.Acme.com" },
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := cfg.Domains["production"]; got != "app.acme.com" {
		t.Fatalf("Domains[production] = %q, want %q (lowercased)", got, "app.acme.com")
	}
}

func TestResolve_NoDomainsYieldsEmptyMap(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Domains) != 0 {
		t.Fatalf("Domains = %v, want empty", cfg.Domains)
	}
}

func TestResolve_ReturnsConfigDirectory(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
};
`)

	cfg, err := Resolve(filepath.Join(root))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Dir != root {
		t.Fatalf("Dir = %q, want %q", cfg.Dir, root)
	}
}

func TestResolve_DefaultsDiscoveryPaths(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_456",
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "ocel" {
		t.Fatalf("Discovery.Paths = %v, want [ocel]", cfg.Discovery.Paths)
	}
}

func TestResolve_MissingConfig(t *testing.T) {
	root := t.TempDir()

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %q, want it to mention `ocel init`", err.Error())
	}
}

func TestResolve_UnparseableConfig(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_789"
  this is not valid typescript +++
`)

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %q, want it to mention `ocel init`", err.Error())
	}
}

func TestResolve_MissingProjectID(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  discovery: { paths: ["x"] },
};
`)

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %q, want it to mention `ocel init`", err.Error())
	}
}

func TestResolve_ParsesProviderDescriptor(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  provider: { package: "@ocel/provider-aws", options: { region: "us-east-1" } },
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Provider == nil {
		t.Fatal("Provider = nil, want a descriptor")
	}
	if cfg.Provider.Package != "@ocel/provider-aws" {
		t.Fatalf("Provider.Package = %q, want %q", cfg.Provider.Package, "@ocel/provider-aws")
	}
	if got, want := string(cfg.Provider.Options), `{"region":"us-east-1"}`; got != want {
		t.Fatalf("Provider.Options = %s, want %s", got, want)
	}
}

func TestResolve_ProviderOptionsDefaultToEmptyObject(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  provider: { package: "@ocel/provider-aws" },
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := string(cfg.Provider.Options), `{}`; got != want {
		t.Fatalf("Provider.Options = %s, want %s", got, want)
	}
}

func TestResolve_ProviderAbsentByDefault(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Provider != nil {
		t.Fatalf("Provider = %+v, want nil", cfg.Provider)
	}
}

func TestConfig_RequireProvider_ErrorWhenAbsent(t *testing.T) {
	cfg := &Config{}

	_, err := cfg.RequireProvider()
	if err == nil {
		t.Fatal("RequireProvider: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no provider configured") {
		t.Fatalf("err = %q, want it to mention %q", err.Error(), "no provider configured")
	}
	if !strings.Contains(err.Error(), "awsProvider") {
		t.Fatalf("err = %q, want it to mention %q", err.Error(), "awsProvider")
	}
}

func TestConfig_RequireProvider_ReturnsDescriptorWhenPresent(t *testing.T) {
	cfg := &Config{Provider: &ProviderDescriptor{Package: "@ocel/provider-aws", Options: []byte(`{}`)}}

	provider, err := cfg.RequireProvider()
	if err != nil {
		t.Fatalf("RequireProvider: %v", err)
	}
	if provider.Package != "@ocel/provider-aws" {
		t.Fatalf("Package = %q, want %q", provider.Package, "@ocel/provider-aws")
	}
}

func TestResolve_ParsesApps(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  apps: [
    { name: "api", path: "services/api", framework: "express", entrypoint: "src/main.ts" },
    { name: "web", path: "services/web", framework: "express" },
  ],
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Apps) != 2 {
		t.Fatalf("Apps = %v, want 2 entries", cfg.Apps)
	}

	api := cfg.Apps[0]
	if api.Name != "api" || api.Path != "services/api" || api.Framework != "express" || api.Entrypoint != "src/main.ts" {
		t.Fatalf("Apps[0] = %+v, unexpected fields", api)
	}

	web := cfg.Apps[1]
	if web.Name != "web" || web.Path != "services/web" || web.Framework != "express" || web.Entrypoint != "" {
		t.Fatalf("Apps[1] = %+v, unexpected fields", web)
	}
}

func TestResolve_AppsAbsentByDefault(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Apps) != 0 {
		t.Fatalf("Apps = %v, want empty", cfg.Apps)
	}
}

func TestResolve_AppComputeDefaultsToServerless(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  apps: [{ name: "api", path: "services/api", framework: "express" }],
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Apps[0].Compute != "serverless" {
		t.Fatalf("Apps[0].Compute = %q, want %q", cfg.Apps[0].Compute, "serverless")
	}
}

func TestResolve_AppComputeNotSettableViaConfig(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  apps: [{ name: "api", path: "services/api", framework: "express", compute: "vm" }],
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Apps[0].Compute != "serverless" {
		t.Fatalf("Apps[0].Compute = %q, want %q — compute must not be user-settable", cfg.Apps[0].Compute, "serverless")
	}
}

func TestResolve_AppDuplicateNamesError(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  apps: [
    { name: "api", path: "services/api", framework: "express" },
    { name: "api", path: "services/other", framework: "express" },
  ],
};
`)

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "api") {
		t.Fatalf("err = %q, want it to name the duplicate %q", err.Error(), "api")
	}
}

// An app name becomes a directory in the build output and part of every one of
// that app's function logical names, so a name that could escape the output
// tree has to be rejected at the config boundary.
func TestResolve_AppUnsafeNameErrors(t *testing.T) {
	for _, name := range []string{"../escape", "web/admin", "we b"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, `
export default {
  projectId: "proj_123",
  apps: [{ name: "`+name+`", path: "services/api", framework: "express" }],
};
`)

			_, err := Resolve(root)
			if err == nil {
				t.Fatal("Resolve: expected error, got nil")
			}
			if !strings.Contains(err.Error(), "invalid app name") {
				t.Fatalf("err = %q, want it to reject the app name", err.Error())
			}
		})
	}
}

func TestResolve_AppMissingPathErrors(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  apps: [{ name: "api", framework: "express" }],
};
`)

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "path") {
		t.Fatalf("err = %q, want it to mention missing %q", err.Error(), "path")
	}
}

func TestResolve_WritesBuildArtifactsUnderOcelDir(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
};
`)

	if _, err := Resolve(root); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	artifact := filepath.Join(root, buildDirName, "config.mjs")
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("expected build artifact at %s: %v", artifact, err)
	}
}

func TestResolve_ParsesPerAppDomain(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  domains: { production: "acme.com" },
  apps: [
    { name: "web", path: "apps/web", framework: "express", domains: { production: "App.Acme.com" } },
    { name: "admin", path: "apps/admin", framework: "express" },
  ],
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Apps) != 2 {
		t.Fatalf("got %d apps, want 2", len(cfg.Apps))
	}
	if got := cfg.Apps[0].Domains["production"]; got != "app.acme.com" {
		t.Fatalf("Apps[0].Domains[production] = %q, want %q (lowercased)", got, "app.acme.com")
	}
	if len(cfg.Apps[1].Domains) != 0 {
		t.Fatalf("Apps[1].Domains = %v, want empty", cfg.Apps[1].Domains)
	}
	// The project-level domain is independent of any app's.
	if got := cfg.Domains["production"]; got != "acme.com" {
		t.Fatalf("Domains[production] = %q, want %q", got, "acme.com")
	}
}
