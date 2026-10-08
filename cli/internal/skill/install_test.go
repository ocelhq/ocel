package skill

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTheSkillDirsAreTheClaudeAndTheAgentsSkillDirsUnderABase(t *testing.T) {
	base := t.TempDir()

	want := []string{
		filepath.Join(base, ".claude", "skills", "ocel"),
		filepath.Join(base, ".agents", "skills", "ocel"),
	}
	if got := Dirs(base); !slices.Equal(got, want) {
		t.Errorf("Dirs = %v, want %v", got, want)
	}
}

func TestListFilesNamesEveryEmbeddedFileSorted(t *testing.T) {
	got := ListFiles()

	if want := listFiles(t, Files()); !slices.Equal(got, want) {
		t.Errorf("ListFiles = %v, want %v", got, want)
	}
}

func TestWriteCopiesTheSkillWithTheVersionStampedIntoSKILLMD(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".claude", "skills", "ocel")

	if err := Write(dir, "1.2.3"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := listFiles(t, os.DirFS(dir)); !slices.Equal(got, ListFiles()) {
		t.Errorf("wrote %v, want %v", got, ListFiles())
	}
	for _, name := range ListFiles() {
		if name == manifestName {
			continue
		}
		written, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		embeddedFile, _ := fs.ReadFile(Files(), name)
		if !bytes.Equal(written, embeddedFile) {
			t.Errorf("%s differs from the embedded file", name)
		}
	}
	if version, err := ReadVersion(dir); err != nil || version != "1.2.3" {
		t.Errorf("ReadVersion = %q, %v, want 1.2.3", version, err)
	}
}

func TestWriteReplacesWhatTheDirHeldBefore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ocel")
	if err := Write(dir, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "references", "removed.md")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Write(dir, "2.0.0"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat %s = %v, want it removed", stale, err)
	}
	if version, _ := ReadVersion(dir); version != "2.0.0" {
		t.Errorf("version = %q, want 2.0.0", version)
	}
}

func TestStampAddsTheVersionToTheFrontmatterMetadataAndKeepsTheRest(t *testing.T) {
	source := "---\nname: ocel\ndescription: Deploys apps.\n---\n\n# Ocel\n"

	got, err := stamp([]byte(source), "0.4.0")
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}

	front, body := splitFrontmatter(t, string(got))
	var parsed struct {
		Name        string            `yaml:"name"`
		Description string            `yaml:"description"`
		Metadata    map[string]string `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(front), &parsed); err != nil {
		t.Fatalf("stamped frontmatter is not YAML: %v\n%s", err, front)
	}
	if parsed.Name != "ocel" || parsed.Description != "Deploys apps." || parsed.Metadata[versionKey] != "0.4.0" {
		t.Errorf("stamped frontmatter = %+v, want name, description and metadata.%s 0.4.0", parsed, versionKey)
	}
	if body != "\n# Ocel\n" {
		t.Errorf("body = %q, want it unchanged", body)
	}
}

func TestStampAddsTheVersionBesideMetadataTheFrontmatterAlreadyHas(t *testing.T) {
	source := "---\nname: ocel\nmetadata:\n  author: ocel\n---\nbody\n"

	got, err := stamp([]byte(source), "0.4.0")
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}

	front, _ := splitFrontmatter(t, string(got))
	var parsed struct {
		Metadata map[string]string `yaml:"metadata"`
	}
	if err := yaml.Unmarshal([]byte(front), &parsed); err != nil {
		t.Fatalf("stamped frontmatter is not YAML: %v\n%s", err, front)
	}
	if parsed.Metadata["author"] != "ocel" || parsed.Metadata[versionKey] != "0.4.0" {
		t.Errorf("metadata = %v, want author kept and %s 0.4.0", parsed.Metadata, versionKey)
	}
}

func TestStampStampsAVersionAsAYAMLString(t *testing.T) {
	got, err := stamp([]byte("---\nname: ocel\n---\n"), "1.0")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(got), versionKey+`: "1.0"`) {
		t.Errorf("stamped = %q, want the version quoted", got)
	}
}

func TestStampRefusesASkillWithoutFrontmatter(t *testing.T) {
	if _, err := stamp([]byte("# Ocel\n"), "1.0.0"); err == nil {
		t.Error("stamp accepted a SKILL.md with no frontmatter")
	}
}

func TestReadVersionOfADirWithNoSkillIsNotExist(t *testing.T) {
	if _, err := ReadVersion(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadVersion err = %v, want fs.ErrNotExist", err)
	}
}

func TestReadVersionOfAnUnstampedSkillIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, manifestName), []byte("---\nname: ocel\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if version, err := ReadVersion(dir); err != nil || version != "" {
		t.Errorf("ReadVersion = %q, %v, want empty", version, err)
	}
}

func splitFrontmatter(t *testing.T, text string) (front, body string) {
	t.Helper()
	rest, found := strings.CutPrefix(text, "---\n")
	if !found {
		t.Fatalf("%q does not open with frontmatter", text)
	}
	front, body, found = strings.Cut(rest, "---\n")
	if !found {
		t.Fatalf("%q does not close its frontmatter", text)
	}
	return front, body
}
