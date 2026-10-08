package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/skill"
)

func TestDoctorSaysNothingOfTheSkillWhenNoneIsInstalled(t *testing.T) {
	t.Parallel()

	if got := skillChecks(t.TempDir(), "0.4.0"); len(got) != 0 {
		t.Errorf("skillChecks = %+v, want none", got)
	}
}

func TestDoctorPassesASkillStampedWithTheCLIVersion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, dir := range skill.Dirs(root) {
		if err := skill.Write(dir, "0.4.0"); err != nil {
			t.Fatal(err)
		}
	}

	got := skillChecks(root, "0.4.0")
	if len(got) != 1 || got[0].verdict != verdictPass {
		t.Errorf("skillChecks = %+v, want one pass", got)
	}
}

func TestDoctorWarnsOfASkillStampedWithAnotherVersion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := skill.Write(skill.Dirs(root)[0], "0.4.0"); err != nil {
		t.Fatal(err)
	}
	if err := skill.Write(skill.Dirs(root)[1], "0.3.0"); err != nil {
		t.Fatal(err)
	}

	got := skillChecks(root, "0.4.0")
	if len(got) != 1 || got[0].verdict != verdictWarn {
		t.Fatalf("skillChecks = %+v, want one warning", got)
	}
	if !strings.Contains(got[0].text, "0.3.0") || !strings.Contains(got[0].text, filepath.Join(".agents", "skills", "ocel")) {
		t.Errorf("text = %q, want the stale directory and its version", got[0].text)
	}
	if got[0].fix != "run `ocel skill install --yes`" {
		t.Errorf("fix = %q, want ocel skill install --yes", got[0].fix)
	}
}

func TestDoctorPassesASkillInstalledWithoutAVersionStamp(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := skill.Dirs(root)[0]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: ocel\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := skillChecks(root, "0.4.0")
	if len(got) != 1 || got[0].verdict != verdictPass {
		t.Errorf("skillChecks = %+v, want one pass for a skill installed without a stamp", got)
	}
}
