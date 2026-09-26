package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/version"
)

func manifestDir(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, manifest), []byte("\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", manifest, err)
		}
	}
	return dir
}

func TestInitWritesAConfigTheLoaderAccepts(t *testing.T) {
	manifests := map[string]struct {
		manifest string
		add      []string
	}{
		"go":     {"go.mod", []string{"go", "get", "ocel.dev"}},
		"rust":   {"Cargo.toml", []string{"cargo", "add", "ocel-sdk"}},
		"python": {"pyproject.toml", []string{"uv", "add", "ocel"}},
		"node":   {"package.json", []string{"npm", "install", "ocel"}},
	}
	for language, want := range manifests {
		t.Run(language, func(t *testing.T) {
			dir := manifestDir(t, want.manifest)
			deps := newDeps()
			argv := stubPackageManager(&deps, nil)

			var stdout bytes.Buffer
			if err := runInit(context.Background(), deps, dir, "acme", initOptions{provider: "aws"}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("runInit: %v — %s", err, stdout.String())
			}

			cfg, err := projectconfig.Resolve(context.Background(), dir, "")
			if err != nil {
				t.Fatalf("the config init wrote does not load: %v", err)
			}
			if cfg.Slug != "acme" || cfg.Provider == nil || cfg.Provider.ID != "aws" {
				t.Fatalf("config = %+v", cfg)
			}
			if strings.Join(*argv, " ") != strings.Join(want.add, " ") {
				t.Fatalf("added the sdk with %v, want %v", *argv, want.add)
			}
		})
	}
}

func TestInitWritesTheSchemaThisCLIShipsWith(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	deps := newDeps()
	stubPackageManager(&deps, nil)

	if err := runInit(context.Background(), deps, dir, "acme", initOptions{provider: "aws"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, projectconfig.DefaultFileName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var doc struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(doc.Schema, version.Version) || !strings.HasSuffix(doc.Schema, "ocel.schema.json") {
		t.Fatalf("$schema = %q, want it to name this CLI's version and the schema", doc.Schema)
	}
}

func TestInitWritesTypeScriptOnRequest(t *testing.T) {
	dir := manifestDir(t, "package.json")
	deps := newDeps()
	stubPackageManager(&deps, nil)

	if err := runInit(context.Background(), deps, dir, "acme", initOptions{provider: "aws", ts: true}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, projectconfig.DefaultFileName)); err == nil {
		t.Fatal("--ts wrote a JSON config as well")
	}
	written, err := os.ReadFile(filepath.Join(dir, projectconfig.TSFileName))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(written), `from "ocel/providers/aws"`) {
		t.Fatalf("config =\n%s", written)
	}
}

func TestInitWritesYAMLOnRequest(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	deps := newDeps()
	stubPackageManager(&deps, nil)

	if err := runInit(context.Background(), deps, dir, "007", initOptions{provider: "aws", yaml: true}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, projectconfig.DefaultFileName)); err == nil {
		t.Fatal("--yaml wrote a JSON config as well")
	}
	cfg, err := projectconfig.Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("the config init wrote does not load: %v", err)
	}
	if cfg.Path != filepath.Join(dir, projectconfig.YAMLFileName) || cfg.Slug != "007" || cfg.Provider == nil || cfg.Provider.ID != "aws" {
		t.Fatalf("config = %+v", cfg)
	}
	written, err := os.ReadFile(cfg.Path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(written), "$schema=https://ocel.dev/schema/"+version.Version+"/ocel.schema.json") {
		t.Fatalf("config names no schema for this CLI's version:\n%s", written)
	}
}

func TestInitRefusesToWriteASecondFormOfTheConfig(t *testing.T) {
	for _, tc := range []struct {
		existing string
		opts     initOptions
		refused  string
	}{
		{projectconfig.DefaultFileName, initOptions{provider: "aws", yaml: true}, projectconfig.YAMLFileName},
		{projectconfig.TSFileName, initOptions{provider: "aws", yaml: true}, projectconfig.YAMLFileName},
		{"ocel.yml", initOptions{provider: "aws", yaml: true}, projectconfig.YAMLFileName},
		{projectconfig.YAMLFileName, initOptions{provider: "aws"}, projectconfig.DefaultFileName},
		{projectconfig.YAMLFileName, initOptions{provider: "aws", ts: true}, projectconfig.TSFileName},
	} {
		t.Run(tc.existing+" then "+tc.refused, func(t *testing.T) {
			dir := manifestDir(t, "go.mod")
			if err := os.WriteFile(filepath.Join(dir, tc.existing), []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("write %s: %v", tc.existing, err)
			}
			deps := newDeps()
			stubPackageManager(&deps, nil)

			err := runInit(context.Background(), deps, dir, "acme", tc.opts, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.existing) {
				t.Fatalf("error %v does not name the %s already there", err, tc.existing)
			}
			if _, err := os.Stat(filepath.Join(dir, tc.refused)); err == nil {
				t.Fatalf("wrote %s beside %s", tc.refused, tc.existing)
			}
		})
	}
}

func TestInitRefusesAFormFlagTheExplicitPathContradicts(t *testing.T) {
	for _, tc := range []struct {
		path string
		opts initOptions
		want string
	}{
		{projectconfig.DefaultFileName, initOptions{provider: "aws", yaml: true}, "--yaml"},
		{"ocel.aws.yaml", initOptions{provider: "aws", ts: true}, "--ts"},
		{"config.json", initOptions{provider: "aws"}, "config.json"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			dir := manifestDir(t, "go.mod")
			deps := newDeps()
			stubPackageManager(&deps, nil)
			tc.opts.configPath = tc.path

			err := runInit(context.Background(), deps, dir, "acme", tc.opts, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("error %v names neither %s nor %s", err, tc.want, tc.path)
			}
			if _, err := os.Stat(filepath.Join(dir, tc.path)); err == nil {
				t.Fatalf("wrote %s", tc.path)
			}
		})
	}
}

func TestInitWritesYAMLToAnExplicitYAMLPath(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	deps := newDeps()
	stubPackageManager(&deps, nil)

	opts := initOptions{provider: "aws", yaml: true, configPath: "ocel.aws.yml"}
	if err := runInit(context.Background(), deps, dir, "acme", opts, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	cfg, err := projectconfig.Resolve(context.Background(), dir, "ocel.aws.yml")
	if err != nil {
		t.Fatalf("the config init wrote does not load: %v", err)
	}
	if cfg.Slug != "acme" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestInitRefusesADirectoryOfSeveralLanguages(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	deps := newDeps()
	stubPackageManager(&deps, nil)

	err := runInit(context.Background(), deps, dir, "acme", initOptions{provider: "aws"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("init picked a language from two manifests")
	}
	for _, want := range []string{"go", "rust", "--lang"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestInitTakesTheLanguageItIsGiven(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("\n"), 0o644); err != nil {
		t.Fatalf("write Cargo.toml: %v", err)
	}
	deps := newDeps()
	argv := stubPackageManager(&deps, nil)

	if err := runInit(context.Background(), deps, dir, "acme", initOptions{provider: "aws", language: "rust"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if strings.Join(*argv, " ") != "cargo add ocel-sdk" {
		t.Fatalf("added the sdk with %v", *argv)
	}
}

func TestInitWritesOnlyTheConfigWhenNoManifestNamesALanguage(t *testing.T) {
	dir := manifestDir(t, "")
	deps := newDeps()
	argv := stubPackageManager(&deps, nil)

	var stdout bytes.Buffer
	if err := runInit(context.Background(), deps, dir, "acme", initOptions{provider: "aws"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("runInit: %v", err)
	}
	if len(*argv) != 0 {
		t.Fatalf("ran %v in a directory with no manifest", *argv)
	}
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		t.Fatal("init wrote a package.json into a directory that had none")
	}
	if _, err := projectconfig.Resolve(context.Background(), dir, ""); err != nil {
		t.Fatalf("the config init wrote does not load: %v", err)
	}
}

func TestInitNamesTheProviderAloneWhereItNeedsNoOptionsAndKeysItElsewhere(t *testing.T) {
	for _, tc := range []struct {
		opts    initOptions
		written string
		want    string
	}{
		{initOptions{provider: "aws"}, projectconfig.DefaultFileName, "  \"provider\": \"aws\"\n"},
		{initOptions{provider: "aws", yaml: true}, projectconfig.YAMLFileName, "provider: aws\n"},
		{initOptions{provider: "vps"}, projectconfig.DefaultFileName, "  \"provider\": { \"vps\": {} }\n"},
		{initOptions{provider: "vps", yaml: true}, projectconfig.YAMLFileName, "provider:\n  vps: {}\n"},
		{initOptions{provider: "aws", ts: true}, projectconfig.TSFileName, "  provider: awsProvider({}),\n"},
	} {
		t.Run(tc.opts.provider+" in "+tc.written, func(t *testing.T) {
			dir := manifestDir(t, "")
			deps := newDeps()
			stubPackageManager(&deps, nil)

			if err := runInit(context.Background(), deps, dir, "acme", tc.opts, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("runInit: %v", err)
			}
			written, err := os.ReadFile(filepath.Join(dir, tc.written))
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			if !strings.Contains(string(written), tc.want) {
				t.Fatalf("config =\n%s\nwant it to contain\n%s", written, tc.want)
			}
		})
	}
}

func TestInitRefusesAProviderOcelDoesNotShip(t *testing.T) {
	dir := manifestDir(t, "go.mod")
	deps := newDeps()
	stubPackageManager(&deps, nil)

	err := runInit(context.Background(), deps, dir, "acme", initOptions{provider: "azure"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("init wrote a config naming a provider nothing ships")
	}
	for _, want := range []string{`"azure"`, "aws, gcp, vps"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, projectconfig.DefaultFileName)); err == nil {
		t.Fatal("a config was written for a provider nothing ships")
	}
}
