package projectconfig

import (
	"context"
	"encoding/json"
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
  aws: { region: eu-west-2 }
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
	if cfg.Provider == nil || cfg.Provider.ID != "aws" {
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
		"aliased int key":  {"slug: acme\nprovider:\n  vps:\n    ssh: &n 1\n    *n : x\n", `has the key 1 under "provider.vps"`},
		"infinite number":  {"slug: acme\nprovider:\n  aws: { weight: .inf }\n", `sets "provider.aws.weight" to +Inf`},
		"duplicate key":    {"slug: acme\nslug: other\n", "already defined"},
		"not an object":    {"- slug: acme\n", "must be an object"},
		"empty":            {"", "must be an object"},
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

func TestResolveKeepsTheSourceTextOfWhatJSONHasNoTypeFor(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, YAMLFileName), `slug: 2024-01-01
provider:
  vps:
    since: 2024-01-01
    at: 2001-12-14t21:59:43.10-05:00
    blob: !!binary aGVsbG8=
    quoted: "1.10"
    version: 1.10
    port: 22
    enabled: true
    yes: yes
    1: one
    2024-06-01: dated
`)

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Slug != "2024-01-01" {
		t.Fatalf("slug = %q, want the date as written", cfg.Slug)
	}
	var options map[string]any
	if err := json.Unmarshal(cfg.Provider.Options, &options); err != nil {
		t.Fatalf("options: %v", err)
	}
	want := map[string]any{
		"since":      "2024-01-01",
		"at":         "2001-12-14t21:59:43.10-05:00",
		"blob":       "aGVsbG8=",
		"quoted":     "1.10",
		"version":    1.1,
		"port":       float64(22),
		"enabled":    true,
		"yes":        "yes",
		"1":          "one",
		"2024-06-01": "dated",
	}
	for key, value := range want {
		if options[key] != value {
			t.Errorf("options[%q] = %#v, want %#v", key, options[key], value)
		}
	}
}

func TestResolveReadsAYAMLNumberAsTheSameJSONNumberWouldBeRead(t *testing.T) {
	yamlDir, jsonDir := t.TempDir(), t.TempDir()
	write(t, filepath.Join(yamlDir, YAMLFileName), "slug: acme\nprovider:\n  vps: { version: 1.10, port: 22 }\n")
	write(t, filepath.Join(jsonDir, DefaultFileName), `{"slug":"acme","provider":{"vps":{"version":1.10,"port":22}}}`)

	fromYAML, err := Resolve(context.Background(), yamlDir, "")
	if err != nil {
		t.Fatalf("resolve yaml: %v", err)
	}
	fromJSON, err := Resolve(context.Background(), jsonDir, "")
	if err != nil {
		t.Fatalf("resolve json: %v", err)
	}
	if string(fromYAML.Provider.Options) != string(fromJSON.Provider.Options) {
		t.Fatalf("yaml options %s, json options %s", fromYAML.Provider.Options, fromJSON.Provider.Options)
	}
}

func TestResolveReadsAYAMLFileWithEmptyDocumentsAroundItsOne(t *testing.T) {
	for name, source := range map[string]string{
		"trailing separator": "slug: acme\nprovider: aws\n---\n",
		"leading separator":  "---\nslug: acme\n",
		"null document":      "slug: acme\n---\n~\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, YAMLFileName), source)

			cfg, err := Resolve(context.Background(), dir, "")
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if cfg.Slug != "acme" {
				t.Fatalf("slug = %q", cfg.Slug)
			}
		})
	}
}

func TestResolveReportsTheSameYAMLErrorOnEveryRun(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, YAMLFileName), `slug: acme
provider:
  vps:
    z: .inf
    a: .nan
    m: -.inf
`)

	_, first := Resolve(context.Background(), dir, "")
	if first == nil || !strings.Contains(first.Error(), `sets "provider.vps.a" to NaN`) {
		t.Fatalf("error %v does not name the first bad key in order", first)
	}
	for range 50 {
		if _, err := Resolve(context.Background(), dir, ""); err == nil || err.Error() != first.Error() {
			t.Fatalf("error %v differs from the first run's %v", err, first)
		}
	}
}

func TestResolveReadsYAMLSelectorsNamedAloneOrKeyed(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, YAMLFileName), `slug: acme
provider: aws
edge: cloudflare
dns:
  cloudflare:
    zone: example.com
`)

	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if cfg.Provider == nil || cfg.Provider.ID != "aws" || string(cfg.Provider.Options) != `{}` {
		t.Fatalf("provider = %+v, want aws with no options", cfg.Provider)
	}
	if cfg.EdgeID() != "cloudflare" {
		t.Fatalf("edge = %q, want cloudflare", cfg.EdgeID())
	}
	if cfg.DNS == nil || cfg.DNS.ID != "cloudflare" || cfg.DNS.Zone != "example.com" {
		t.Fatalf("dns = %+v, want cloudflare in example.com", cfg.DNS)
	}
}

func TestResolveRefusesAYAMLProviderKeyedWithNoValue(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, YAMLFileName), "slug: acme\nprovider:\n  aws:\n")

	_, err := Resolve(context.Background(), dir, "")
	if err == nil || !strings.Contains(err.Error(), `"provider.aws" must be an object of options`) {
		t.Fatalf("error %v, want aws with no value refused as options that are not an object", err)
	}
}
