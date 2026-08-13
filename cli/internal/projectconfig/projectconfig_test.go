package projectconfig

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func nestedDir(t *testing.T, root string) string {
	t.Helper()
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	return nested
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

func TestFindProjectRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(t *testing.T, root string)
		wantStart bool
	}{
		{
			name: "walks up to the config file",
			setup: func(t *testing.T, root string) {
				writeConfig(t, root, "export default { slug: \"test-app\" };")
			},
		},
		{
			name: "walks up to the scratch dir",
			setup: func(t *testing.T, root string) {
				if err := os.MkdirAll(filepath.Join(root, scratchDirName), 0o755); err != nil {
					t.Fatalf("mkdir scratch: %v", err)
				}
			},
		},
		{
			name:      "falls back to the start dir when nothing anchors the walk",
			setup:     func(t *testing.T, root string) {},
			wantStart: true,
		},
		{
			name: "ignores a scratch dir that is a file rather than a directory",
			setup: func(t *testing.T, root string) {
				if err := os.WriteFile(filepath.Join(root, scratchDirName), []byte("x"), 0o644); err != nil {
					t.Fatalf("write scratch file: %v", err)
				}
			},
			wantStart: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			tc.setup(t, root)
			start := nestedDir(t, root)

			found, err := findProjectRoot(start)
			if err != nil {
				t.Fatalf("findProjectRoot: %v", err)
			}
			if tc.wantStart {
				if found != start {
					t.Fatalf("root = %q, want the start directory %q", found, start)
				}
				return
			}
			if found != root {
				t.Fatalf("root = %q, want %q", found, root)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	accepted := []struct {
		name   string
		config string
		check  func(t *testing.T, root string, cfg *Config)
	}{
		{
			name: "parses a valid config",
			config: `
export default {
  slug: "test-app",
  discovery: { paths: ["resources"] },
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if cfg.Slug != "test-app" {
					t.Fatalf("Slug = %q, want %q", cfg.Slug, "test-app")
				}
				if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "resources" {
					t.Fatalf("Discovery.Paths = %v, want [resources]", cfg.Discovery.Paths)
				}
			},
		},
		{
			name: "parses and lowercases the production domain",
			config: `
export default {
  slug: "test-app",
  domains: { production: "App.Acme.com" },
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if got := cfg.Domains["production"]; len(got) != 1 || got[0] != "app.acme.com" {
					t.Fatalf("Domains[production] = %v, want [%q] (lowercased)", got, "app.acme.com")
				}
			},
		},
		{
			name: "yields an empty map when no domains are declared",
			config: `
export default {
  slug: "test-app",
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if len(cfg.Domains) != 0 {
					t.Fatalf("Domains = %v, want empty", cfg.Domains)
				}
			},
		},
		{
			name: "returns the config directory",
			config: `
export default {
  slug: "test-app",
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if cfg.Dir != root {
					t.Fatalf("Dir = %q, want %q", cfg.Dir, root)
				}
			},
		},
		{
			name: "defaults the discovery paths",
			config: `
export default {
  slug: "test-app",
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "ocel" {
					t.Fatalf("Discovery.Paths = %v, want [ocel]", cfg.Discovery.Paths)
				}
			},
		},
		{
			name: "ignores a leftover project ID",
			config: `
export default {
  slug: "test-app",
  projectId: "proj_123",
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if cfg.Slug != "test-app" {
					t.Fatalf("Slug = %q, want %q", cfg.Slug, "test-app")
				}
			},
		},
		{
			name: "accepts a valid slug",
			config: `
export default {
  slug: "acme-web-1",
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if cfg.Slug != "acme-web-1" {
					t.Errorf("Slug = %q, want acme-web-1", cfg.Slug)
				}
			},
		},
		{
			name: "parses the provider descriptor",
			config: `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: { region: "us-east-1" } },
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if cfg.Provider == nil {
					t.Fatal("Provider = nil, want a descriptor")
				}
				if cfg.Provider.Package != "@ocel/provider-aws" {
					t.Fatalf("Provider.Package = %q, want %q", cfg.Provider.Package, "@ocel/provider-aws")
				}
				if got, want := string(cfg.Provider.Options), `{"region":"us-east-1"}`; got != want {
					t.Fatalf("Provider.Options = %s, want %s", got, want)
				}
			},
		},
		{
			name: "defaults the provider options to an empty object",
			config: `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws" },
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if got, want := string(cfg.Provider.Options), `{}`; got != want {
					t.Fatalf("Provider.Options = %s, want %s", got, want)
				}
			},
		},
		{
			name: "leaves the provider absent by default",
			config: `
export default {
  slug: "test-app",
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if cfg.Provider != nil {
					t.Fatalf("Provider = %+v, want nil", cfg.Provider)
				}
			},
		},
		{
			name: "parses apps",
			config: `
export default {
  slug: "test-app",
  apps: [
    { name: "api", path: "services/api", framework: "express", entrypoint: "src/main.ts" },
    { name: "web", path: "services/web", framework: "express" },
  ],
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
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
			},
		},
		{
			name: "leaves apps absent by default",
			config: `
export default {
  slug: "test-app",
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if len(cfg.Apps) != 0 {
					t.Fatalf("Apps = %v, want empty", cfg.Apps)
				}
			},
		},
		{
			name: "defaults app compute to serverless",
			config: `
export default {
  slug: "test-app",
  apps: [{ name: "api", path: "services/api", framework: "express" }],
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if cfg.Apps[0].Compute != "serverless" {
					t.Fatalf("Apps[0].Compute = %q, want %q", cfg.Apps[0].Compute, "serverless")
				}
			},
		},
		{
			name: "does not let the config set app compute",
			config: `
export default {
  slug: "test-app",
  apps: [{ name: "api", path: "services/api", framework: "express", compute: "vm" }],
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if cfg.Apps[0].Compute != "serverless" {
					t.Fatalf("Apps[0].Compute = %q, want %q — compute must not be user-settable", cfg.Apps[0].Compute, "serverless")
				}
			},
		},
		{
			name: "writes build artifacts under the ocel dir",
			config: `
export default {
  slug: "test-app",
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				artifact := filepath.Join(root, scratchDirName, "config.mjs")
				if _, err := os.Stat(artifact); err != nil {
					t.Fatalf("expected build artifact at %s: %v", artifact, err)
				}
			},
		},
		{
			name: "parses a per-app domain",
			config: `
export default {
  slug: "test-app",
  domains: { production: "acme.com" },
  apps: [
    { name: "web", path: "apps/web", framework: "express", domains: { production: "App.Acme.com" } },
    { name: "admin", path: "apps/admin", framework: "express" },
  ],
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if len(cfg.Apps) != 2 {
					t.Fatalf("got %d apps, want 2", len(cfg.Apps))
				}
				if got := cfg.Apps[0].Domains["production"]; len(got) != 1 || got[0] != "app.acme.com" {
					t.Fatalf("Apps[0].Domains[production] = %v, want [%q] (lowercased)", got, "app.acme.com")
				}
				if len(cfg.Apps[1].Domains) != 0 {
					t.Fatalf("Apps[1].Domains = %v, want empty", cfg.Apps[1].Domains)
				}
				if got := cfg.Domains["production"]; len(got) != 1 || got[0] != "acme.com" {
					t.Fatalf("Domains[production] = %v, want [%q]", got, "acme.com")
				}
			},
		},
		{
			name: "keeps a per-app production domain legal",
			config: `
export default {
  slug: "test-app",
  domains: { preview: "*.preview.acme.com" },
  apps: [
    { name: "web", path: "apps/web", framework: "express", domains: { production: "app.acme.com" } },
  ],
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if got := cfg.Apps[0].Domains["production"]; len(got) != 1 || got[0] != "app.acme.com" {
					t.Fatalf("Apps[0].Domains[production] = %v, want [app.acme.com]", got)
				}
			},
		},
		{
			name: "parses a preview wildcard domain",
			config: `
export default {
  slug: "test-app",
  domains: { production: "acme.com", preview: "*.Preview.Acme.com" },
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if got := cfg.Domains["preview"]; len(got) != 1 || got[0] != "*.preview.acme.com" {
					t.Fatalf("Domains[preview] = %v, want [%q] (lowercased)", got, "*.preview.acme.com")
				}
			},
		},
		{
			name: "accepts a list of production domains",
			config: `
export default {
  slug: "test-app",
  domains: { production: ["Acme.com", "www.acme.com", "acme.com"] },
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if got := cfg.Domains["production"]; len(got) != 2 || got[0] != "acme.com" || got[1] != "www.acme.com" {
					t.Fatalf("Domains[production] = %v, want [acme.com www.acme.com]", got)
				}
			},
		},
		{
			name: "binds an app to a folder",
			config: `
export default {
  slug: "test-app",
  apps: [
    { name: "web", path: "apps/web", framework: "next", folder: "/web" },
    { name: "admin", path: "apps/admin", framework: "next" },
  ],
};
`,
			check: func(t *testing.T, root string, cfg *Config) {
				if cfg.Apps[0].Folder != "/web" {
					t.Errorf("Apps[0].Folder = %q, want %q", cfg.Apps[0].Folder, "/web")
				}
				if cfg.Apps[1].Folder != "" {
					t.Errorf("Apps[1].Folder = %q, want empty: an app that binds nothing reads the project root", cfg.Apps[1].Folder)
				}
			},
		},
	}

	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeConfig(t, root, tc.config)

			cfg, err := Resolve(context.Background(), root)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			tc.check(t, root, cfg)
		})
	}

	rejected := []struct {
		name    string
		config  string
		wantErr []string
	}{
		{
			name: "rejects an unparseable config and points at ocel init",
			config: `
export default {
  slug: "test-app",
  this is not valid typescript +++
`,
			wantErr: []string{"ocel init"},
		},
		{
			name: "names the missing slug",
			config: `
export default {
};
`,
			wantErr: []string{"slug"},
		},
		{
			name: "rejects duplicate app names",
			config: `
export default {
  slug: "test-app",
  apps: [
    { name: "api", path: "services/api", framework: "express" },
    { name: "api", path: "services/other", framework: "express" },
  ],
};
`,
			wantErr: []string{"api"},
		},
		{
			name: "rejects an app with no path",
			config: `
export default {
  slug: "test-app",
  apps: [{ name: "api", framework: "express" }],
};
`,
			wantErr: []string{"path"},
		},
		{
			name: "rejects a per-app preview domain",
			config: `
export default {
  slug: "test-app",
  apps: [
    { name: "web", path: "apps/web", framework: "express", domains: { preview: "*.preview.acme.com" } },
  ],
};
`,
			wantErr: []string{`app "web"`, "domains.preview", "project", "ocel domains"},
		},
		{
			name: "rejects a non-wildcard preview domain",
			config: `
export default {
  slug: "test-app",
  domains: { preview: "preview.acme.com" },
};
`,
		},
		{
			name: "rejects a production domain identical to the preview wildcard",
			config: `
export default {
  slug: "test-app",
  domains: { production: ["*.acme.com"], preview: "*.acme.com" },
};
`,
		},
		{
			name: "rejects two apps binding the same folder",
			config: `
export default {
  slug: "test-app",
  apps: [
    { name: "web", path: "apps/web", framework: "next", folder: "/shared" },
    { name: "admin", path: "apps/admin", framework: "next", folder: "/shared" },
  ],
};
`,
			wantErr: []string{"/shared"},
		},
	}

	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeConfig(t, root, tc.config)

			_, err := Resolve(context.Background(), root)
			if err == nil {
				t.Fatalf("Resolve: expected error, got nil — %s", tc.name)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to mention %q", err, want)
				}
			}
		})
	}

	t.Run("missing config points at ocel init", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()

		_, err := Resolve(context.Background(), root)
		if err == nil {
			t.Fatal("Resolve: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %q, want it to mention `ocel init`", err.Error())
		}
	})

	t.Run("a scratch dir is not a config", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, scratchDirName), 0o755); err != nil {
			t.Fatalf("mkdir scratch: %v", err)
		}

		_, err := Resolve(context.Background(), nestedDir(t, root))
		if err == nil {
			t.Fatal("Resolve: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "ocel init") {
			t.Fatalf("err = %q, want it to mention `ocel init`", err.Error())
		}
	})

	t.Run("rejects an invalid slug", func(t *testing.T) {
		t.Parallel()

		for _, bad := range []string{"Has_Underscore", "UPPER", "-leading", "trailing-", "has space"} {
			t.Run(bad, func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()
				writeConfig(t, root, `
export default {
  slug: "`+bad+`",
};
`)
				_, err := Resolve(context.Background(), root)
				if err == nil || !strings.Contains(err.Error(), "slug") {
					t.Fatalf("Resolve(slug=%q) err = %v, want a slug validation error", bad, err)
				}
			})
		}
	})

	t.Run("rejects a slug carrying the field separator", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "shop--web",
};
`)
		_, err := Resolve(context.Background(), root)
		if err == nil {
			t.Fatal("Resolve(slug=shop--web) err = nil, want a refusal")
		}
		for _, want := range []string{"shop--web", `"--"`, "single hyphen"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to contain %q", err, want)
			}
		}
	})

	t.Run("rejects an unsafe app name", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"..", "../escape", "web/admin", `web\\admin`, "/abs"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()
				writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "`+name+`", path: "services/api", framework: "express" }],
};
`)

				_, err := Resolve(context.Background(), root)
				if err == nil {
					t.Fatal("Resolve: expected error, got nil")
				}
				if !strings.Contains(err.Error(), "invalid app name") {
					t.Fatalf("err = %q, want it to reject the app name", err.Error())
				}
			})
		}
	})

	t.Run("rejects an app name that is not a DNS label", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"web.app", "we b", "app.v2", "-web", "web-", "Web", "web_admin", strings.Repeat("a", 64)} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()
				writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "`+name+`", path: "services/api", framework: "express" }],
};
`)

				_, err := Resolve(context.Background(), root)
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
	})

	t.Run("allows an app name that is a DNS label", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"web", "web-admin", "app2", "2app", "a", strings.Repeat("a", 63)} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()
				writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "`+name+`", path: "services/api", framework: "express" }],
};
`)

				cfg, err := Resolve(context.Background(), root)
				if err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				if len(cfg.Apps) != 1 || cfg.Apps[0].Name != name {
					t.Fatalf("Apps = %v, want the app named %q", cfg.Apps, name)
				}
			})
		}
	})

	t.Run("an app folder must be a well shaped path", func(t *testing.T) {
		t.Parallel()

		for folder, want := range map[string]string{
			"web":         "must start with",
			"/web/":       "must not end with",
			"/web//admin": "empty path segment",
			"/we#b":       `may not contain "#"`,
			"/":           "is the project root",
		} {
			t.Run(folder, func(t *testing.T) {
				t.Parallel()

				root := t.TempDir()
				writeConfig(t, root, `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "apps/web", framework: "next", folder: "`+folder+`" }],
};
`)

				_, err := Resolve(context.Background(), root)
				if err == nil {
					t.Fatalf("Resolve(folder=%q) err = nil, want a rejection", folder)
				}
				if !strings.Contains(err.Error(), folder) || !strings.Contains(err.Error(), want) {
					t.Errorf("err = %v, want it to name %q and say %q", err, folder, want)
				}
			})
		}
	})
}

func TestResolveOptional(t *testing.T) {
	t.Parallel()

	t.Run("no config yields defaults rooted at the project root", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()

		cfg, err := ResolveOptional(context.Background(), root)
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
	})

	t.Run("no config anchors on the scratch dir", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, scratchDirName), 0o755); err != nil {
			t.Fatalf("mkdir scratch: %v", err)
		}

		cfg, err := ResolveOptional(context.Background(), nestedDir(t, root))
		if err != nil {
			t.Fatalf("ResolveOptional: %v", err)
		}
		if cfg.Dir != root {
			t.Fatalf("Dir = %q, want %q", cfg.Dir, root)
		}
	})

	t.Run("a config still wins", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `
export default {
  slug: "test-app",
  discovery: { paths: ["resources"] },
};
`)

		cfg, err := ResolveOptional(context.Background(), nestedDir(t, root))
		if err != nil {
			t.Fatalf("ResolveOptional: %v", err)
		}
		if cfg.Dir != root {
			t.Fatalf("Dir = %q, want %q", cfg.Dir, root)
		}
		if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "resources" {
			t.Fatalf("Discovery.Paths = %v, want [resources]", cfg.Discovery.Paths)
		}
	})

	t.Run("an unparseable config still errors", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeConfig(t, root, `export default { this is not valid typescript +++`)

		if _, err := ResolveOptional(context.Background(), root); err == nil {
			t.Fatal("ResolveOptional: expected error, got nil")
		}
	})
}

func TestResolveReturnsPromptlyOnCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake node")
	}

	root := t.TempDir()
	writeConfig(t, root, `
export default {
  slug: "test-app",
};
`)

	fakePathDir := t.TempDir()
	fakeNode := filepath.Join(fakePathDir, "node")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake node: %v", err)
	}
	t.Setenv("PATH", fakePathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := Resolve(ctx, root)
		done <- err
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Resolve() err = nil, want an error from the cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Resolve did not return promptly after the context was cancelled")
	}
}

func TestValidSlug(t *testing.T) {
	t.Parallel()

	t.Run("accepts well formed slugs", func(t *testing.T) {
		t.Parallel()

		for _, s := range []string{"a", "acme", "acme-web-1", "1", strings.Repeat("a", 63)} {
			if !ValidSlug(s) {
				t.Errorf("ValidSlug(%q) = false, want true", s)
			}
		}
	})

	t.Run("rejects malformed slugs", func(t *testing.T) {
		t.Parallel()

		invalid := []string{
			"",
			"UPPER",
			"Has_Underscore",
			"-leading",
			"trailing-",
			"has space",
			"has.dot",
			"a--b",
			"double--separator--everywhere",
			strings.Repeat("a", 64),
		}
		for _, s := range invalid {
			if ValidSlug(s) {
				t.Errorf("ValidSlug(%q) = true, want false", s)
			}
		}
	})
}

func TestConfigRequireProvider(t *testing.T) {
	t.Parallel()

	t.Run("errors when the provider is absent", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("returns the descriptor when the provider is present", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: &ProviderDescriptor{Package: "@ocel/provider-aws", Options: []byte(`{}`)}}

		provider, err := cfg.RequireProvider()
		if err != nil {
			t.Fatalf("RequireProvider: %v", err)
		}
		if provider.Package != "@ocel/provider-aws" {
			t.Fatalf("Package = %q, want %q", provider.Package, "@ocel/provider-aws")
		}
	})
}

func TestValidatePreviewDomain(t *testing.T) {
	t.Parallel()

	t.Run("accepts a single leading wildcard label", func(t *testing.T) {
		t.Parallel()

		for _, d := range []string{"*.preview.acme.com", "*.acme.com"} {
			if err := ValidatePreviewDomain(d); err != nil {
				t.Errorf("ValidatePreviewDomain(%q) = %v, want nil", d, err)
			}
		}
	})

	t.Run("rejects anything else", func(t *testing.T) {
		t.Parallel()

		invalid := []string{
			"preview.acme.com",
			"acme.com",
			"*.*.acme.com",
			"foo*.preview.com",
			"preview.*.acme.com",
			"*",
		}
		for _, d := range invalid {
			if err := ValidatePreviewDomain(d); err == nil {
				t.Errorf("ValidatePreviewDomain(%q) = nil, want error", d)
			}
		}
	})
}

func TestPreviewBaseDomain(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"*.preview.acme.com": "preview.acme.com",
		"*.acme.com":         "acme.com",
		"acme.com":           "",
		"":                   "",
	}
	for in, want := range cases {
		if got := PreviewBaseDomain(in); got != want {
			t.Errorf("PreviewBaseDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeProductionDomains(t *testing.T) {
	t.Parallel()

	t.Run("dedupes and normalizes hostnames", func(t *testing.T) {
		t.Parallel()

		got, err := normalizeProductionDomains(stringOrList{"App.com", "app.com", " www.app.com "}, "")
		if err != nil {
			t.Fatalf("normalizeProductionDomains: %v", err)
		}
		if len(got) != 2 || got[0] != "app.com" || got[1] != "www.app.com" {
			t.Fatalf("normalized = %v, want [app.com www.app.com]", got)
		}
	})

	t.Run("rejects a preview collision", func(t *testing.T) {
		t.Parallel()

		if _, err := normalizeProductionDomains(stringOrList{"*.app.com"}, "*.app.com"); err == nil {
			t.Fatal("want error when a production hostname equals the preview wildcard")
		}
	})
}
