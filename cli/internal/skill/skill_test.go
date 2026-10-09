package skill

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const sourceDir = "../../../skills/ocel"

func listFiles(t *testing.T, tree fs.FS) []string {
	t.Helper()
	var names []string
	err := fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			names = append(names, name)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

func TestTheEmbeddedSkillHoldsEveryFileOfTheSkillInTheRepository(t *testing.T) {
	want := listFiles(t, os.DirFS(sourceDir))
	if !slices.Contains(want, "SKILL.md") {
		t.Fatalf("%s holds %v, want a SKILL.md", sourceDir, want)
	}
	if got := listFiles(t, Files()); !slices.Equal(got, want) {
		t.Fatalf("embedded skill holds %v, want %v; run go generate -C cli ./...", got, want)
	}
	for _, name := range want {
		got, err := fs.ReadFile(Files(), name)
		if err != nil {
			t.Fatal(err)
		}
		source, err := os.ReadFile(filepath.Join(sourceDir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, source) {
			t.Errorf("embedded %s differs from the one in %s; run go generate -C cli ./...", name, sourceDir)
		}
	}
}

func TestEveryUserFacingResourceKindHasAReferenceInTheSkill(t *testing.T) {
	plumbing := []resourcesv1.ResourceType{
		resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED,
		resourcesv1.ResourceType_RESOURCE_TYPE_WORKER,
		resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER,
	}
	values := resourcesv1.ResourceType(0).Descriptor().Values()
	for i := range values.Len() {
		kind := resourcesv1.ResourceType(values.Get(i).Number())
		if slices.Contains(plumbing, kind) {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(kind.String(), "RESOURCE_TYPE_")) + ".md"
		if _, err := os.Stat(filepath.Join(sourceDir, "references", name)); err != nil {
			t.Errorf("%s has no reference: want %s in %s/references, or the kind in the plumbing list", kind, name, sourceDir)
		}
	}
}
