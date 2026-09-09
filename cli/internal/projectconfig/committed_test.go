package projectconfig_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tailscale/hujson"

	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

const committedSchemaFile = "www/public/schema/ocel.schema.json"

const compareTable = "www/components/compare/data.ts"

var sampleNamed = regexp.MustCompile("filename: \"([^\"]+)\",\\s*code: `([^`]*)`")

var skippedDirs = map[string]bool{
	"node_modules": true,
	".ocel":        true,
	".next":        true,
	"dist":         true,
	"output":       true,
	".git":         true,
}

func schemaID(t *testing.T, root string) string {
	t.Helper()
	var read struct {
		ID string `json:"$id"`
	}
	data, err := os.ReadFile(filepath.Join(root, committedSchemaFile))
	if err != nil {
		t.Fatalf("read the committed schema: %v", err)
	}
	if err := json.Unmarshal(data, &read); err != nil {
		t.Fatalf("unmarshal the committed schema: %v", err)
	}
	return read.ID
}

func committedSchema(t *testing.T, root string) *jsonschema.Schema {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, committedSchemaFile))
	if err != nil {
		t.Fatalf("read the committed schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse the committed schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(committedSchemaFile, doc); err != nil {
		t.Fatalf("add the committed schema: %v", err)
	}
	schema, err := compiler.Compile(committedSchemaFile)
	if err != nil {
		t.Fatalf("compile the committed schema: %v", err)
	}
	return schema
}

func documentOf(t *testing.T, name string, source []byte) any {
	t.Helper()
	value, err := parseConfig(source)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}

func parseConfig(source []byte) (any, error) {
	standard, err := hujson.Standardize(source)
	if err != nil {
		return nil, fmt.Errorf("is not valid JSON: %w", err)
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(standard))
}

func committedConfigs(t *testing.T, root string) []string {
	t.Helper()
	found := fixturetest.ConfigsIn(t, root)
	for _, dir := range fixturetest.Dirs(t) {
		found = append(found, fixturetest.ConfigsIn(t, dir)...)
	}
	return append(found, fixturetest.ConfigsIn(t, filepath.Join(root, "console", "web"))...)
}

type commentBlock struct{ first, last int }

func commentBlocksIn(lines []string) []commentBlock {
	var blocks []commentBlock
	open := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			if open < 0 {
				open = i
			}
			continue
		}
		if open >= 0 {
			blocks = append(blocks, commentBlock{open, i - 1})
			open = -1
		}
	}
	if open >= 0 {
		blocks = append(blocks, commentBlock{open, len(lines) - 1})
	}
	return blocks
}

func uncommented(lines []string, at commentBlock) []byte {
	variant := lines[at.last]
	indent := variant[:len(variant)-len(strings.TrimLeft(variant, " \t"))]
	out := slices.Clone(lines[:at.first])
	out = append(out, indent+strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(variant), "//")))
	return []byte(strings.Join(append(out, lines[at.last+1:]...), "\n"))
}

func keyPaths(prefix string, value any) []string {
	switch held := value.(type) {
	case map[string]any:
		var paths []string
		for key, item := range held {
			at := prefix + "." + key
			paths = append(paths, at)
			paths = append(paths, keyPaths(at, item)...)
		}
		slices.Sort(paths)
		return paths
	case []any:
		var paths []string
		for i, item := range held {
			paths = append(paths, keyPaths(fmt.Sprintf("%s[%d]", prefix, i), item)...)
		}
		return paths
	default:
		return nil
	}
}

func TestEverySampleConfigTheCompareTableShowsValidatesAgainstTheSchema(t *testing.T) {
	root := fixturetest.RepoDir(t)
	schema := committedSchema(t, root)
	want := schemaID(t, root)
	source, err := os.ReadFile(filepath.Join(root, compareTable))
	if err != nil {
		t.Fatalf("read the compare table: %v", err)
	}
	shown := 0
	for _, sample := range sampleNamed.FindAllStringSubmatch(string(source), -1) {
		if !configTitled(sample[1]) {
			continue
		}
		shown++
		name := compareTable + " › " + sample[1]
		document := documentOf(t, name, []byte(sample[2]))
		named, _ := document.(map[string]any)["$schema"].(string)
		if named != want {
			t.Errorf("%s names %q, want the committed schema %q", name, named, want)
		}
		if err := schema.Validate(document); err != nil {
			t.Errorf("%s does not validate against the committed schema: %v", name, err)
		}
	}
	if shown == 0 {
		t.Fatal("the compare table shows no ocel.json")
	}
}

func TestEveryCommittedConfigValidatesAgainstTheSchema(t *testing.T) {
	root := fixturetest.RepoDir(t)
	schema := committedSchema(t, root)
	for _, path := range committedConfigs(t, root) {
		if projectconfig.IsProgram(path) {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := schema.Validate(documentOf(t, path, source)); err != nil {
			t.Errorf("%s does not validate against the committed schema: %v", path, err)
		}
	}
}

func TestEveryCommittedConfigNamesTheCommittedSchema(t *testing.T) {
	root := fixturetest.RepoDir(t)
	want := schemaID(t, root)
	for _, path := range committedConfigs(t, root) {
		if projectconfig.IsProgram(path) {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var read struct {
			Schema string `json:"$schema"`
		}
		standard, err := hujson.Standardize(source)
		if err != nil {
			t.Fatalf("%s is not valid JSON: %v", path, err)
		}
		if err := json.Unmarshal(standard, &read); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		if read.Schema != want {
			t.Errorf("%s names %q, want the committed schema %q", path, read.Schema, want)
		}
	}
}

func TestACommittedConfigCommentsOneVariantAndOneLineSayingWhenToPickIt(t *testing.T) {
	root := fixturetest.RepoDir(t)
	for _, path := range committedConfigs(t, root) {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		blocks := commentBlocksIn(strings.Split(string(source), "\n"))
		if len(blocks) > 1 {
			t.Errorf("%s comments %d separate things, and a config comments at most one variant", path, len(blocks))
			continue
		}
		for _, at := range blocks {
			if at.last-at.first != 1 {
				t.Errorf("%s comments %d lines, and a variant is one line under one line saying when to pick it", path, at.last-at.first+1)
			}
		}
	}
}

func TestACommentedVariantUncommentsIntoAConfigThatOnlyDiffers(t *testing.T) {
	root := fixturetest.RepoDir(t)
	schema := committedSchema(t, root)
	for _, path := range committedConfigs(t, root) {
		if projectconfig.IsProgram(path) {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		lines := strings.Split(string(source), "\n")
		for _, at := range commentBlocksIn(lines) {
			picked, err := parseConfig(uncommented(lines, at))
			if err != nil {
				t.Errorf("%s picked at line %d %v", path, at.last+1, err)
				continue
			}
			if err := schema.Validate(picked); err != nil {
				t.Errorf("%s picked at line %d does not validate against the committed schema: %v", path, at.last+1, err)
			}
			live := documentOf(t, path, source)
			if want, got := keyPaths("", live), keyPaths("", picked); !slices.Equal(want, got) {
				t.Errorf("%s picked at line %d holds keys %v, and a commented variant conflicts with a live key rather than adding %v", path, at.last+1, got, want)
			}
		}
	}
}

func TestOnlyTheNodeFixtureCarriesTheTypeScriptConfig(t *testing.T) {
	want := filepath.Join(fixturetest.RepoDir(t), "tests", "fixtures", "deploy", "node")
	var carrying []string
	for _, dir := range fixturetest.Dirs(t) {
		for _, path := range fixturetest.ConfigsIn(t, dir) {
			if projectconfig.IsProgram(path) {
				carrying = append(carrying, dir)
				break
			}
		}
	}
	if !slices.Equal(carrying, []string{want}) {
		t.Fatalf("the fixtures carrying a typescript config are %v, want only %s", carrying, want)
	}
}

func TestAFixtureOfAnotherLanguageCarriesNoNodeFiles(t *testing.T) {
	for _, dir := range fixturetest.Dirs(t) {
		if fixturetest.IsNode(t, dir) {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && path != dir && skippedDirs[entry.Name()] {
				return fs.SkipDir
			}
			switch entry.Name() {
			case "package.json", "tsconfig.json", "node_modules":
				t.Errorf("%s is not a node fixture and still carries %s", dir, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

func TestTheGoFixtureDeploysFromJSONAlone(t *testing.T) {
	dir := filepath.Join(fixturetest.RepoDir(t), "tests", "fixtures", "deploy", "go")
	for _, path := range fixturetest.ConfigsIn(t, dir) {
		if projectconfig.IsProgram(path) {
			t.Fatalf("the go fixture still carries %s", filepath.Base(path))
		}
	}

	t.Setenv("PATH", "")
	cfg, err := projectconfig.Resolve(t.Context(), dir, "")
	if err != nil {
		t.Fatalf("resolve the go fixture with no node on PATH: %v", err)
	}
	if cfg.Path != filepath.Join(dir, projectconfig.DefaultFileName) {
		t.Fatalf("path = %q", cfg.Path)
	}
	if cfg.Provider == nil || cfg.Provider.Name != "aws" {
		t.Fatalf("provider = %+v", cfg.Provider)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Runtime.Name != "go" {
		t.Fatalf("apps = %+v", cfg.Apps)
	}
}
