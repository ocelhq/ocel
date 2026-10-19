package telemetry

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func writeProjectFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAStackNamesEachAppsFrameworkLanguageAndTheVersionsItDeclares(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, filepath.Join(root, ".nvmrc"), "22\n")
	writeProjectFile(t, filepath.Join(root, "web", "package.json"), `{"dependencies":{"next":"^15.1.0"}}`)
	writeProjectFile(t, filepath.Join(root, "api", "go.mod"), "module example.com/api\n\ngo 1.23\n")
	cfg := &project.Project{
		Slug:     "shop",
		Dir:      root,
		Provider: &project.Provider{ID: "gcp"},
		Apps: []project.App{
			{Name: "web", Path: "web", Framework: "next", Language: language.JS},
			{Name: "api", Path: "api", Framework: "go", Language: language.Go, Container: &project.Container{}},
		},
	}

	got := NewStack(cfg)

	want := Stack{
		Project:           "shop",
		Provider:          "gcp",
		Frameworks:        []string{"next", "go"},
		Languages:         []string{"js", "go"},
		FrameworkVersions: []string{"next@15"},
		RuntimeVersions:   []string{"node@22", "go@1.23"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stack = %+v, want %+v", got, want)
	}
}

func TestAStackOfAProjectWithNoProviderOrAppsIsEmptyButForItsProject(t *testing.T) {
	got := NewStack(&project.Project{Slug: "shop", Dir: t.TempDir()})

	if !reflect.DeepEqual(got, Stack{Project: "shop"}) {
		t.Errorf("stack = %+v, want only the project", got)
	}
}
