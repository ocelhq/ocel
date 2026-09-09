package projectconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/constants"
)

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveReadsJSONWithoutNode(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{
  // the provider this project deploys into
  "slug": "go-only",
  "provider": { "name": "aws", "options": { "region": "eu-west-2" } },
  "apps": [{ "name": "web", "path": "./server", "runtime": "go" }],
}`)

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "go-only" {
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
	if cfg.Path != filepath.Join(dir, DefaultFileName) {
		t.Fatalf("path = %q", cfg.Path)
	}
}

func TestResolveRefusesBothFormsOfOneBaseName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)
	write(t, filepath.Join(dir, TSFileName), `export default { slug: "acme" };`)

	_, err := Resolve(context.Background(), dir, "")
	if err == nil {
		t.Fatal("resolved a directory holding both forms")
	}
	for _, name := range []string{DefaultFileName, TSFileName} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not name %s", err, name)
		}
	}
}

func TestResolveAllowsAJSONDefaultBesideATSVariant(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)
	write(t, filepath.Join(dir, "ocel.aws.config.ts"), `export default { slug: "acme-aws" };`)

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "acme" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
}

func TestResolveReadsAJSONVariantByName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)
	write(t, filepath.Join(dir, "ocel.vps.json"), `{"slug":"acme-vps"}`)

	cfg, err := Resolve(context.Background(), dir, "ocel.vps.json")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "acme-vps" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
}

func TestResolveRefusesAFileNoLoaderReads(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ocel.jsonc"), `{"slug":"acme"}`)

	_, err := Resolve(context.Background(), dir, "ocel.jsonc")
	if err == nil {
		t.Fatal("resolved a file no loader reads")
	}
	if !strings.Contains(err.Error(), "ocel.jsonc") {
		t.Fatalf("error %q does not name the file", err)
	}
}

func TestResolveNamesTheKeyPathOfATypo(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme","apps":[{"name":"web","path":".","runtim":"go"}]}`)

	_, err := Resolve(context.Background(), dir, "")
	if err == nil {
		t.Fatal("resolved a config with a typo key")
	}
	if !strings.Contains(err.Error(), "apps[0].runtim") {
		t.Fatalf("error %q does not name the key path", err)
	}
}

func TestResolveNamesTheKeyPathOfAMissingVariable(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme","apps":[{"name":"web","path":"${APP_DIR}"}]}`)

	_, err := Resolve(context.Background(), dir, "")
	if err == nil {
		t.Fatal("resolved a config reading an unset variable")
	}
	if !strings.Contains(err.Error(), "apps[0].path") || !strings.Contains(err.Error(), "APP_DIR") {
		t.Fatalf("error %q names neither the key path nor the variable", err)
	}
}

func TestResolveReadsVariablesFromTheEnvironmentOverDotenv(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"${SLUG}","apps":[{"name":"web","path":"${APP_DIR}"}]}`)
	write(t, filepath.Join(dir, ".env"), "SLUG=from-dotenv\nAPP_DIR=./web\n")
	t.Setenv("SLUG", "from-environment")

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "from-environment" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
	if cfg.Apps[0].Path != "./web" {
		t.Fatalf("path = %q", cfg.Apps[0].Path)
	}
}

func TestFindProjectRootStopsAtAJSONConfig(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, DefaultFileName), `{"slug":"acme"}`)
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	found, err := findProjectRoot(nested)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found != root {
		t.Fatalf("root = %q, want %q", found, root)
	}
}

func TestFindProjectRootIgnoresTheScratchDirectory(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a")
	if err := os.MkdirAll(filepath.Join(nested, constants.ProjectStateDirName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, filepath.Join(root, DefaultFileName), `{"slug":"acme"}`)

	found, err := findProjectRoot(nested)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found != root {
		t.Fatalf("root = %q, want %q — a scratch directory no longer anchors the walk", found, root)
	}
}
