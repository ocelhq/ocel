package projectconfig

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveReadsYAMLWithoutNode(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, YAMLFileName), `# the provider this project deploys into
slug: yaml-only
provider:
  name: aws
  options: { region: eu-west-2 }
apps:
  - name: web
    path: ./server
    framework: go
`)
	t.Setenv("PATH", "")

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "yaml-only" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
	if cfg.Provider == nil || cfg.Provider.Name != "aws" {
		t.Fatalf("provider = %+v", cfg.Provider)
	}
	if string(cfg.Provider.Options) != `{"region":"eu-west-2"}` {
		t.Fatalf("options = %s", cfg.Provider.Options)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Runtime.Name != "go" {
		t.Fatalf("apps = %+v", cfg.Apps)
	}
	if cfg.Path != filepath.Join(dir, YAMLFileName) {
		t.Fatalf("path = %q", cfg.Path)
	}
}

func TestResolveReadsTheShortYAMLSuffix(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ocel.yml"), "slug: acme\n")

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "acme" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
}

func TestResolveReadsAYAMLVariantByName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)
	write(t, filepath.Join(dir, "ocel.vps.yaml"), "slug: acme-vps\n")

	cfg, err := Resolve(context.Background(), dir, "ocel.vps.yaml")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "acme-vps" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
}

func TestResolveRefusesYAMLBesideAnotherFormOfOneBaseName(t *testing.T) {
	for _, other := range []string{DefaultFileName, "ocel.yml", TSFileName} {
		t.Run(other, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, YAMLFileName), "slug: acme\n")
			write(t, filepath.Join(dir, other), "slug: acme\n")

			_, err := Resolve(context.Background(), dir, YAMLFileName)
			if err == nil {
				t.Fatalf("resolved a directory holding %s and %s", YAMLFileName, other)
			}
			for _, name := range []string{YAMLFileName, other} {
				if !strings.Contains(err.Error(), name) {
					t.Fatalf("error %q does not name %s", err, name)
				}
			}
		})
	}
}

func TestResolveExpandsAnchorsAndMergeKeysInYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, YAMLFileName), `slug: acme
apps:
  - &app
    name: web
    path: ./server
    framework: go
  - <<: *app
    name: worker
`)

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(cfg.Apps) != 2 || cfg.Apps[1].Name != "worker" || cfg.Apps[1].Path != "./server" || cfg.Apps[1].Runtime.Name != "go" {
		t.Fatalf("apps = %+v", cfg.Apps)
	}
}

func TestResolveNamesTheKeyPathOfATypoInYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, YAMLFileName), "slug: acme\napps:\n  - name: web\n    path: .\n    runtim: go\n")

	_, err := Resolve(context.Background(), dir, "")
	if err == nil {
		t.Fatal("resolved a config with a typo key")
	}
	if !strings.Contains(err.Error(), "apps[0].runtim") {
		t.Fatalf("error %q does not name the key path", err)
	}
}

func TestResolveReadsVariablesInYAML(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, YAMLFileName), "slug: ${SLUG}\n")
	write(t, filepath.Join(dir, ".env"), "SLUG=from-dotenv\n")

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "from-dotenv" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
}

func TestResolveRefusesYAMLItCannotReadAsOneDocument(t *testing.T) {
	for name, tc := range map[string]struct{ source, want string }{
		"malformed":        {"slug: [acme\n", "not valid YAML"},
		"two documents":    {"slug: acme\n---\nslug: other\n", "more than one YAML document"},
		"non-string key":   {"slug: acme\nbindings:\n  1: orders\n", `"bindings" has the key 1`},
		"infinite number":  {"slug: acme\nprovider:\n  name: aws\n  options: { weight: .inf }\n", `"provider.options.weight"`},
		"duplicate key":    {"slug: acme\nslug: other\n", "already defined"},
		"not an object":    {"- slug: acme\n", "must be an object"},
		"empty":            {"", "must be an object"},
		"non-string top":   {"true: acme\n", "the config has the key true"},
		"malformed second": {"slug: acme\n---\nslug: [\n", "not valid YAML"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, YAMLFileName), tc.source)

			_, err := Resolve(context.Background(), dir, "")
			if err == nil {
				t.Fatal("resolved it")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), YAMLFileName) {
				t.Fatalf("error %q names neither %q nor the file", err, tc.want)
			}
		})
	}
}

func TestResolveNamesTheYAMLFormsWhenNoConfigIsFound(t *testing.T) {
	_, err := Resolve(context.Background(), t.TempDir(), "")
	var missing NoConfigError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want a NoConfigError", err)
	}
	for _, name := range []string{DefaultFileName, YAMLFileName, "ocel.yml", TSFileName} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not name %s", err, name)
		}
	}
}
