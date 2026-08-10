package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nestedDir(t *testing.T, root string) string {
	t.Helper()
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	return nested
}

func TestFindProjectRoot_WalksUpToConfigFile(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "export default { slug: \"test-app\" };")

	found, err := findProjectRoot(nestedDir(t, root))
	if err != nil {
		t.Fatalf("findProjectRoot: %v", err)
	}
	if found != root {
		t.Fatalf("root = %q, want %q", found, root)
	}
}

// The first run in a config-less clone anchors at the working directory and
// creates .ocel/ there, so later runs from a subdirectory must find it.
func TestFindProjectRoot_WalksUpToScratchDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, scratchDirName), 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}

	found, err := findProjectRoot(nestedDir(t, root))
	if err != nil {
		t.Fatalf("findProjectRoot: %v", err)
	}
	if found != root {
		t.Fatalf("root = %q, want %q", found, root)
	}
}

func TestFindProjectRoot_FallsBackToStartDir(t *testing.T) {
	start := nestedDir(t, t.TempDir())

	found, err := findProjectRoot(start)
	if err != nil {
		t.Fatalf("findProjectRoot: %v", err)
	}
	if found != start {
		t.Fatalf("root = %q, want the start directory %q", found, start)
	}
}

// A .ocel/ that is a file, not a directory, is not an anchor.
func TestFindProjectRoot_IgnoresScratchDirFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, scratchDirName), []byte("x"), 0o644); err != nil {
		t.Fatalf("write scratch file: %v", err)
	}
	start := nestedDir(t, root)

	found, err := findProjectRoot(start)
	if err != nil {
		t.Fatalf("findProjectRoot: %v", err)
	}
	if found != start {
		t.Fatalf("root = %q, want the start directory %q", found, start)
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
  slug: "test-app",
  discovery: { paths: ["resources"] },
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Slug != "test-app" {
		t.Fatalf("Slug = %q, want %q", cfg.Slug, "test-app")
	}
	if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "resources" {
		t.Fatalf("Discovery.Paths = %v, want [resources]", cfg.Discovery.Paths)
	}
}

func TestResolve_ParsesProductionDomain(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  domains: { production: "App.Acme.com" },
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := cfg.Domains["production"]; len(got) != 1 || got[0] != "app.acme.com" {
		t.Fatalf("Domains[production] = %v, want [%q] (lowercased)", got, "app.acme.com")
	}
}

func TestResolve_NoDomainsYieldsEmptyMap(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
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
  slug: "test-app",
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
  slug: "test-app",
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
  slug: "test-app",
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

// A .ocel/ anchors the project root, but it is not a config: the deploy path
// still needs a real one.
func TestResolve_ScratchDirIsNotAConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, scratchDirName), 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}

	_, err := Resolve(nestedDir(t, root))
	if err == nil {
		t.Fatal("Resolve: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %q, want it to mention `ocel init`", err.Error())
	}
}

// projectId is gone from the config; a leftover one is ignored silently.
func TestResolve_IgnoresLeftoverProjectID(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  projectId: "proj_123",
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Slug != "test-app" {
		t.Fatalf("Slug = %q, want %q", cfg.Slug, "test-app")
	}
}

func TestResolveOptional_NoConfigYieldsDefaultsRootedAtProjectRoot(t *testing.T) {
	root := t.TempDir()

	cfg, err := ResolveOptional(root)
	if err != nil {
		t.Fatalf("ResolveOptional: %v", err)
	}
	if cfg.Dir != root {
		t.Fatalf("Dir = %q, want %q", cfg.Dir, root)
	}
	if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "ocel" {
		t.Fatalf("Discovery.Paths = %v, want [ocel]", cfg.Discovery.Paths)
	}
	if cfg.Slug != "" || cfg.Provider != nil || len(cfg.Apps) != 0 {
		t.Fatalf("cfg = %+v, want the deploy-only fields empty", cfg)
	}
}

// Dev in a config-less subdirectory of a linked clone must resolve to the same
// root the link file lives at, not to the subdirectory.
func TestResolveOptional_NoConfigAnchorsOnScratchDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, scratchDirName), 0o755); err != nil {
		t.Fatalf("mkdir scratch: %v", err)
	}

	cfg, err := ResolveOptional(nestedDir(t, root))
	if err != nil {
		t.Fatalf("ResolveOptional: %v", err)
	}
	if cfg.Dir != root {
		t.Fatalf("Dir = %q, want %q", cfg.Dir, root)
	}
}

func TestResolveOptional_ConfigStillWins(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  discovery: { paths: ["resources"] },
};
`)

	cfg, err := ResolveOptional(nestedDir(t, root))
	if err != nil {
		t.Fatalf("ResolveOptional: %v", err)
	}
	if cfg.Dir != root {
		t.Fatalf("Dir = %q, want %q", cfg.Dir, root)
	}
	if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "resources" {
		t.Fatalf("Discovery.Paths = %v, want [resources]", cfg.Discovery.Paths)
	}
}

// An absent config is fine; a broken one is still an error.
func TestResolveOptional_UnparseableConfigStillErrors(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `export default { this is not valid typescript +++`)

	if _, err := ResolveOptional(root); err == nil {
		t.Fatal("ResolveOptional: expected error, got nil")
	}
}

func TestResolve_MissingSlug(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
};
`)

	_, err := Resolve(root)
	if err == nil || !strings.Contains(err.Error(), "slug") {
		t.Fatalf("Resolve err = %v, want it to name the missing slug", err)
	}
}

// ValidSlug is the single definition of the slug rule, shared with everything
// that mints one, so its boundaries are pinned directly and not only through
// Resolve.
func TestValidSlug(t *testing.T) {
	valid := []string{"a", "acme", "acme-web-1", "1", strings.Repeat("a", 63)}
	for _, s := range valid {
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		"UPPER",
		"Has_Underscore",
		"-leading",
		"trailing-",
		"has space",
		"has.dot",
		strings.Repeat("a", 64),
	}
	for _, s := range invalid {
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false", s)
		}
	}
}

func TestResolve_InvalidSlugRejected(t *testing.T) {
	for _, bad := range []string{"Has_Underscore", "UPPER", "-leading", "trailing-", "has space"} {
		t.Run(bad, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, `
export default {
  slug: "`+bad+`",
};
`)
			_, err := Resolve(root)
			if err == nil || !strings.Contains(err.Error(), "slug") {
				t.Fatalf("Resolve(slug=%q) err = %v, want a slug validation error", bad, err)
			}
		})
	}
}

func TestResolve_ValidSlugAccepted(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "acme-web-1",
};
`)
	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Slug != "acme-web-1" {
		t.Errorf("Slug = %q, want acme-web-1", cfg.Slug)
	}
}

func TestResolve_ParsesProviderDescriptor(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
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
  slug: "test-app",
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
  slug: "test-app",
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
  slug: "test-app",
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
  slug: "test-app",
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
  slug: "test-app",
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
  slug: "test-app",
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
  slug: "test-app",
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
// tree has to be rejected at the config boundary. The DNS-label rule subsumes
// this, but the protection is what app-name validation exists for and must not
// regress.
func TestResolve_AppUnsafeNameErrors(t *testing.T) {
	for _, name := range []string{"..", "../escape", "web/admin", `web\\admin`, "/abs"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, `
export default {
  slug: "test-app",
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

// An app name is spent as a DNS label — the app half of a multi-app preview
// host, "<pointer>--<app>.<base>" — so names that are merely harmless as a
// directory segment are not enough. These four were once explicitly allowed;
// each of them makes a hostname that cannot be parsed or does not exist.
func TestResolve_AppNameRejectsNonDNSLabels(t *testing.T) {
	for _, name := range []string{"web.app", "we b", "app.v2", "-web", "web-", "Web", "web_admin", strings.Repeat("a", 64)} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, `
export default {
  slug: "test-app",
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
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("err = %q, want it to name the offending value %q", err.Error(), name)
			}
		})
	}
}

// The shapes a DNS label allows must keep resolving.
func TestResolve_AppNameAllowsDNSLabels(t *testing.T) {
	for _, name := range []string{"web", "web-admin", "app2", "2app", "a", strings.Repeat("a", 63)} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "`+name+`", path: "services/api", framework: "express" }],
};
`)

			cfg, err := Resolve(root)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if len(cfg.Apps) != 1 || cfg.Apps[0].Name != name {
				t.Fatalf("Apps = %v, want the app named %q", cfg.Apps, name)
			}
		})
	}
}

func TestResolve_AppMissingPathErrors(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
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
  slug: "test-app",
};
`)

	if _, err := Resolve(root); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	artifact := filepath.Join(root, scratchDirName, "config.mjs")
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("expected build artifact at %s: %v", artifact, err)
	}
}

func TestResolve_ParsesPerAppDomain(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
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
	if got := cfg.Apps[0].Domains["production"]; len(got) != 1 || got[0] != "app.acme.com" {
		t.Fatalf("Apps[0].Domains[production] = %v, want [%q] (lowercased)", got, "app.acme.com")
	}
	if len(cfg.Apps[1].Domains) != 0 {
		t.Fatalf("Apps[1].Domains = %v, want empty", cfg.Apps[1].Domains)
	}
	// The project-level domain is independent of any app's.
	if got := cfg.Domains["production"]; len(got) != 1 || got[0] != "acme.com" {
		t.Fatalf("Domains[production] = %v, want [%q]", got, "acme.com")
	}
}

// TestResolve_RejectsPerAppPreviewDomain pins the project-level rule: a preview
// domain binds to the project, which serves every app from one entrypoint
// worker, so an app declaring its own is a config error rather than a silently
// ignored field.
func TestResolve_RejectsPerAppPreviewDomain(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [
    { name: "web", path: "apps/web", framework: "express", domains: { preview: "*.preview.acme.com" } },
  ],
};
`)

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve accepted a per-app preview domain, want an error")
	}
	for _, want := range []string{`app "web"`, "domains.preview", "project", "ocel domains"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

// TestResolve_PerAppProductionDomainStaysLegal guards the other half of the
// rule: only preview is project-level.
func TestResolve_PerAppProductionDomainStaysLegal(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  domains: { preview: "*.preview.acme.com" },
  apps: [
    { name: "web", path: "apps/web", framework: "express", domains: { production: "app.acme.com" } },
  ],
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := cfg.Apps[0].Domains["production"]; len(got) != 1 || got[0] != "app.acme.com" {
		t.Fatalf("Apps[0].Domains[production] = %v, want [app.acme.com]", got)
	}
}

func TestResolve_ParsesPreviewWildcardDomain(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  domains: { production: "acme.com", preview: "*.Preview.Acme.com" },
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := cfg.Domains["preview"]; len(got) != 1 || got[0] != "*.preview.acme.com" {
		t.Fatalf("Domains[preview] = %v, want [%q] (lowercased)", got, "*.preview.acme.com")
	}
}

func TestResolve_RejectsInvalidPreviewDomain(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  domains: { preview: "preview.acme.com" },
};
`)

	if _, err := Resolve(root); err == nil {
		t.Fatal("Resolve accepted a non-wildcard preview domain, want error")
	}
}

func TestValidatePreviewDomain(t *testing.T) {
	valid := []string{
		"*.preview.acme.com",
		"*.acme.com",
	}
	for _, d := range valid {
		if err := validatePreviewDomain(d); err != nil {
			t.Errorf("validatePreviewDomain(%q) = %v, want nil", d, err)
		}
	}

	invalid := []string{
		"preview.acme.com",   // no wildcard
		"acme.com",           // apex, no wildcard
		"*.*.acme.com",       // multiple wildcards
		"foo*.preview.com",   // wildcard not a whole label
		"preview.*.acme.com", // wildcard not leftmost
		"*",                  // no base domain
	}
	for _, d := range invalid {
		if err := validatePreviewDomain(d); err == nil {
			t.Errorf("validatePreviewDomain(%q) = nil, want error", d)
		}
	}
}

func TestPreviewBaseDomain(t *testing.T) {
	cases := map[string]string{
		"*.preview.acme.com": "preview.acme.com",
		"*.acme.com":         "acme.com",
		"acme.com":           "", // not a wildcard
		"":                   "",
	}
	for in, want := range cases {
		if got := PreviewBaseDomain(in); got != want {
			t.Errorf("PreviewBaseDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolve_ProductionAcceptsListOfDomains(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  domains: { production: ["Acme.com", "www.acme.com", "acme.com"] },
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Lowercased, and the duplicate "acme.com" deduped in declared order.
	if got := cfg.Domains["production"]; len(got) != 2 || got[0] != "acme.com" || got[1] != "www.acme.com" {
		t.Fatalf("Domains[production] = %v, want [acme.com www.acme.com]", got)
	}
}

func TestResolve_RejectsProductionDomainEqualToPreview(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  domains: { production: ["*.acme.com"], preview: "*.acme.com" },
};
`)

	if _, err := Resolve(root); err == nil {
		t.Fatal("Resolve accepted a production domain identical to the preview wildcard, want error")
	}
}

func TestNormalizeProductionDomains_DedupesAndRejectsPreviewCollision(t *testing.T) {
	got, err := normalizeProductionDomains(stringOrList{"App.com", "app.com", " www.app.com "}, "")
	if err != nil {
		t.Fatalf("normalizeProductionDomains: %v", err)
	}
	if len(got) != 2 || got[0] != "app.com" || got[1] != "www.app.com" {
		t.Fatalf("normalized = %v, want [app.com www.app.com]", got)
	}

	if _, err := normalizeProductionDomains(stringOrList{"*.app.com"}, "*.app.com"); err == nil {
		t.Fatal("want error when a production hostname equals the preview wildcard")
	}
}

func TestResolve_AppBindsToAFolder(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [
    { name: "web", path: "apps/web", framework: "next", folder: "/web" },
    { name: "admin", path: "apps/admin", framework: "next" },
  ],
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Apps[0].Folder != "/web" {
		t.Errorf("Apps[0].Folder = %q, want %q", cfg.Apps[0].Folder, "/web")
	}
	if cfg.Apps[1].Folder != "" {
		t.Errorf("Apps[1].Folder = %q, want empty: an app that binds nothing reads the project root", cfg.Apps[1].Folder)
	}
}

func TestResolve_AppFolderMustBeAWellShapedPath(t *testing.T) {
	for folder, want := range map[string]string{
		"web":         "must start with",
		"/web/":       "must not end with",
		"/web//admin": "empty path segment",
		"/we#b":       `may not contain "#"`,
		"/":           "is the project root",
	} {
		t.Run(folder, func(t *testing.T) {
			root := t.TempDir()
			writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "apps/web", framework: "next", folder: "`+folder+`" }],
};
`)

			_, err := Resolve(root)
			if err == nil {
				t.Fatalf("Resolve(folder=%q) err = nil, want a rejection", folder)
			}
			if !strings.Contains(err.Error(), folder) || !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q and say %q", err, folder, want)
			}
		})
	}
}

func TestResolve_TwoAppsCannotBindTheSameFolder(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [
    { name: "web", path: "apps/web", framework: "next", folder: "/shared" },
    { name: "admin", path: "apps/admin", framework: "next", folder: "/shared" },
  ],
};
`)

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve err = nil, want two apps sharing one folder rejected")
	}
	if !strings.Contains(err.Error(), "/shared") {
		t.Errorf("err = %v, want it to name the folder both apps bound", err)
	}
}
