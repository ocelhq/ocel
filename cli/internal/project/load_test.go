package project

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/statedir"
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

func TestLoadReadsJSONWithoutNode(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{
  // the provider this project deploys into
  "slug": "go-only",
  "provider": { "fake": { "size": "large" } },
  "apps": [{ "name": "web", "path": "./server", "compute": { "serverless": { "framework": "go" } } }],
}`)

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "go-only" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
	if cfg.Provider == nil || cfg.Provider.ID != "fake" {
		t.Fatalf("provider = %+v", cfg.Provider)
	}
	if string(cfg.Provider.Options) != `{"size":"large"}` {
		t.Fatalf("options = %s", cfg.Provider.Options)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Framework() != "go" {
		t.Fatalf("apps = %+v", cfg.Apps)
	}
	if cfg.Path != filepath.Join(dir, DefaultFileName) {
		t.Fatalf("path = %q", cfg.Path)
	}
}

func TestLoadRefusesBothFormsOfOneBaseName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)
	write(t, filepath.Join(dir, TSFileName), `export default { slug: "acme" };`)

	_, err := Load(context.Background(), dir, "")
	if err == nil {
		t.Fatal("resolved a directory containing both forms")
	}
	for _, name := range []string{DefaultFileName, TSFileName} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not name %s", err, name)
		}
	}
}

func TestLoadAllowsAJSONDefaultBesideATSVariant(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)
	write(t, filepath.Join(dir, "ocel.staging.config.ts"), `export default { slug: "acme-staging" };`)

	cfg, err := Load(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "acme" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
}

func TestLoadReadsAJSONVariantByName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme"}`)
	write(t, filepath.Join(dir, "ocel.staging.json"), `{"slug":"acme-staging"}`)

	cfg, err := Load(context.Background(), dir, "ocel.staging.json")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "acme-staging" {
		t.Fatalf("slug = %q", cfg.Slug)
	}
}

func TestLoadRefusesAFileNoLoaderReads(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ocel.jsonc"), `{"slug":"acme"}`)

	_, err := Load(context.Background(), dir, "ocel.jsonc")
	if err == nil {
		t.Fatal("resolved a file no loader reads")
	}
	if !strings.Contains(err.Error(), "ocel.jsonc") {
		t.Fatalf("error %q does not name the file", err)
	}
}

func TestLoadNamesTheKeyPathOfATypo(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme","apps":[{"name":"web","path":".","runtim":"go"}]}`)

	_, err := Load(context.Background(), dir, "")
	if err == nil {
		t.Fatal("resolved a config with a typo key")
	}
	if !strings.Contains(err.Error(), "apps[0].runtim") {
		t.Fatalf("error %q does not name the key path", err)
	}
}

func TestLoadNamesTheKeyPathOfAMissingVariable(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"acme","apps":[{"name":"web","path":"${APP_DIR}"}]}`)

	_, err := Load(context.Background(), dir, "")
	if err == nil {
		t.Fatal("resolved a config reading an unset variable")
	}
	if !strings.Contains(err.Error(), "apps[0].path") || !strings.Contains(err.Error(), "APP_DIR") {
		t.Fatalf("error %q names neither the key path nor the variable", err)
	}
}

func TestLoadReadsVariablesFromTheEnvironmentOverDotenv(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, DefaultFileName), `{"slug":"${SLUG}","apps":[{"name":"web","path":"${APP_DIR}"}]}`)
	write(t, filepath.Join(dir, ".env"), "SLUG=from-dotenv\nAPP_DIR=./web\n")
	t.Setenv("SLUG", "from-environment")

	cfg, err := Load(context.Background(), dir, "")
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
	if err := os.MkdirAll(filepath.Join(nested, statedir.Name), 0o755); err != nil {
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

func TestFindRootIsTheDirectoryOfTheNearestConfig(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, DefaultFileName), `{"slug":"acme"}`)
	nested := filepath.Join(root, "a")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if found, err := FindRoot(nested, ""); err != nil || found != root {
		t.Fatalf("FindRoot = %q, %v, want %q", found, err, root)
	}
}

func TestFindRootIsTheStartDirectoryWhenNoConfigIsFound(t *testing.T) {
	start := t.TempDir()

	if found, err := FindRoot(start, ""); err != nil || found != start {
		t.Fatalf("FindRoot = %q, %v, want %q", found, err, start)
	}
}

func TestFindRootIsTheDirectoryOfAnExplicitConfig(t *testing.T) {
	start, elsewhere := t.TempDir(), t.TempDir()
	write(t, filepath.Join(elsewhere, "ocel.staging.json"), `{"slug":"acme"}`)

	if found, err := FindRoot(start, filepath.Join(elsewhere, "ocel.staging.json")); err != nil || found != elsewhere {
		t.Fatalf("FindRoot = %q, %v, want %q", found, err, elsewhere)
	}
}

func TestLoadRefusesTheSameSelectorsInEveryForm(t *testing.T) {
	cases := []struct {
		name string
		json string
		yaml string
		ts   string
		want []string
	}{
		{
			name: "a provider keyed twice",
			json: `{"slug":"acme","provider":{"fake":{},"other":{"host":"box"}}}`,
			yaml: "slug: acme\nprovider:\n  fake: {}\n  other:\n    host: box\n",
			ts:   `export default { slug: "acme", provider: { fake: {}, other: { host: "box" } } };`,
			want: []string{`"provider" is keyed by fake and other, and a project has one provider — keep one of ` + strings.Join(configdoc.ProviderIDs(), ", ")},
		},
		{
			name: "an edge nobody fronts with",
			json: `{"slug":"acme","provider":{"fake":{"edge":"unknown-edge"}}}`,
			yaml: "slug: acme\nprovider:\n  fake:\n    edge: unknown-edge\n",
			ts:   `export default { slug: "acme", provider: { fake: { edge: "unknown-edge" } } };`,
			want: []string{`"provider.fake.edge" names "unknown-edge", which fake cannot front deployments with — name one of relay, direct`},
		},
		{
			name: "a dns keyed by nothing ocel writes with",
			json: `{"slug":"acme","provider":{"fake":{"dns":{"unknown-dns":{}}}}}`,
			yaml: "slug: acme\nprovider:\n  fake:\n    dns:\n      unknown-dns: {}\n",
			ts:   `export default { slug: "acme", provider: { fake: { dns: { "unknown-dns": {} } } } };`,
			want: []string{`"provider.fake.dns" names "unknown-dns", which fake cannot write hostname records with — name one of zone`},
		},
	}
	for _, c := range cases {
		for name, contents := range map[string]string{DefaultFileName: c.json, YAMLFileName: c.yaml, TSFileName: c.ts} {
			t.Run(c.name+" in "+name, func(t *testing.T) {
				dir := t.TempDir()
				write(t, filepath.Join(dir, name), contents)

				_, err := Load(context.Background(), dir, "")
				if err == nil {
					t.Fatalf("error nil, want %q", c.want)
				}
				for _, want := range c.want {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %v, want %q", err, want)
					}
				}
			})
		}
	}
}
