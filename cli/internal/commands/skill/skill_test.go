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

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/skill"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
)

func setUpProject(t *testing.T) (root, nested string) {
	t.Helper()
	root = clitest.SetUpProject(t).Root
	nested = filepath.Join(root, "web", "src")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, nested
}

func install(t *testing.T, invocation commands.Invocation, opts installOptions, cwd string) string {
	t.Helper()
	var stdout bytes.Buffer
	if err := runInstall(invocation, opts, cwd, "1.2.3", &stdout); err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	return stdout.String()
}

func assertAbsent(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want nothing written", dir, err)
		}
	}
}

func TestInstallWithoutYesListsTheFilesItWouldWriteUnderTheProjectRootAndWritesNothing(t *testing.T) {
	root, nested := setUpProject(t)

	out := install(t, clitest.NewInvocation(), installOptions{}, nested)

	assertAbsent(t, skill.Dirs(root)...)
	for _, dir := range skill.Dirs(root) {
		for _, name := range skill.ListFiles() {
			if want := filepath.Join(dir, filepath.FromSlash(name)); !strings.Contains(out, want) {
				t.Errorf("listing does not name %s:\n%s", want, out)
			}
		}
	}
	if !strings.Contains(out, "ocel skill install --yes") {
		t.Errorf("listing does not say how to write the files:\n%s", out)
	}
}

func TestInstallWithoutYesNamesTheExplicitConfigInTheCommandThatWrites(t *testing.T) {
	root, _ := setUpProject(t)
	elsewhere := filepath.Join(t.TempDir(), "my app")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(elsewhere, "ocel.json")
	if err := os.WriteFile(config, []byte(`{"slug":"other","provider":"aws"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	invocation := clitest.NewInvocation()
	invocation.ConfigPath = func() string { return config }

	out := install(t, invocation, installOptions{}, root)

	if want := "ocel skill install --config '" + config + "' --yes"; !strings.Contains(out, want) {
		t.Errorf("listing does not say %q:\n%s", want, out)
	}
}

func TestInstallWithYesWritesTheSkillIntoBothProjectSkillDirs(t *testing.T) {
	root, nested := setUpProject(t)

	out := install(t, clitest.NewInvocation(), installOptions{yes: true}, nested)

	for _, dir := range skill.Dirs(root) {
		if version, err := skill.ReadVersion(dir); err != nil || version != "1.2.3" {
			t.Errorf("ReadVersion(%s) = %q, %v, want the skill stamped 1.2.3", dir, version, err)
		}
		if !strings.Contains(out, dir) {
			t.Errorf("output does not name %s:\n%s", dir, out)
		}
	}
}

func TestInstallOutsideAProjectTargetsTheWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()

	install(t, clitest.NewInvocation(), installOptions{yes: true}, cwd)

	for _, dir := range skill.Dirs(cwd) {
		if _, err := skill.ReadVersion(dir); err != nil {
			t.Errorf("ReadVersion(%s): %v", dir, err)
		}
	}
}

func TestInstallRunTwiceLeavesTheSameFilesAndNothingElse(t *testing.T) {
	root, _ := setUpProject(t)
	install(t, clitest.NewInvocation(), installOptions{yes: true}, root)
	dir := skill.Dirs(root)[0]
	stray := filepath.Join(dir, "stray.md")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(filepath.Dir(dir), "other", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(beside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(beside, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}

	install(t, clitest.NewInvocation(), installOptions{yes: true}, root)

	assertAbsent(t, stray)
	var got []string
	err := fs.WalkDir(os.DirFS(dir), ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			got = append(got, name)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, skill.ListFiles()) {
		t.Errorf("%s holds %v, want exactly %v", dir, got, skill.ListFiles())
	}
	if content, err := os.ReadFile(beside); err != nil || string(content) != "other" {
		t.Errorf("another skill beside it = %q, %v, want it untouched", content, err)
	}
}

func TestInstallGlobalTargetsTheHomeSkillDirsInstead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root, _ := setUpProject(t)

	install(t, clitest.NewInvocation(), installOptions{yes: true, global: true}, root)

	for _, dir := range skill.Dirs(home) {
		if version, err := skill.ReadVersion(dir); err != nil || version != "1.2.3" {
			t.Errorf("ReadVersion(%s) = %q, %v, want the skill stamped 1.2.3", dir, version, err)
		}
	}
	assertAbsent(t, skill.Dirs(root)...)
}

func TestInstallAsJSONWithoutYesPrintsThePlannedFilesUnwritten(t *testing.T) {
	root, _ := setUpProject(t)
	invocation := clitest.NewInvocation()
	invocation.Presentation = clitest.ResolveJSONPresentation

	out := install(t, invocation, installOptions{}, root)

	var got resultv1.SkillInstallResult
	clitest.DecodeResultInto(t, out, &got)
	if got.GetWritten() || got.GetOcelVersion() != "1.2.3" {
		t.Errorf("result = %v, want written false and version 1.2.3", &got)
	}
	assertTargets(t, got.GetTargets(), skill.Dirs(root))
	assertAbsent(t, skill.Dirs(root)...)
}

func TestInstallAsJSONWithYesPrintsTheFilesItWrote(t *testing.T) {
	root, _ := setUpProject(t)
	invocation := clitest.NewInvocation()
	invocation.Presentation = clitest.ResolveJSONPresentation

	out := install(t, invocation, installOptions{yes: true}, root)

	var got resultv1.SkillInstallResult
	clitest.DecodeResultInto(t, out, &got)
	if !got.GetWritten() {
		t.Errorf("result = %v, want written true", &got)
	}
	assertTargets(t, got.GetTargets(), skill.Dirs(root))
}

func assertTargets(t *testing.T, targets []*resultv1.SkillTarget, dirs []string) {
	t.Helper()
	if len(targets) != len(dirs) {
		t.Fatalf("targets = %v, want one per %v", targets, dirs)
	}
	for i, target := range targets {
		if target.GetDir() != dirs[i] || !slices.Equal(target.GetFiles(), skill.ListFiles()) {
			t.Errorf("target %d = %v, want %s with %v", i, target, dirs[i], skill.ListFiles())
		}
	}
}
