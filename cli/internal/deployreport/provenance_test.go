package deployreport

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"buf.build/go/protovalidate"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestARunOutsideCIIsTriggeredByTheCLIAndNamesNoCI(t *testing.T) {
	t.Parallel()

	trigger, ci := ReadTrigger(environment(nil), "")

	if trigger.GetKind() != consolev1.TriggerKind_TRIGGER_KIND_CLI || trigger.GetActor() != "" {
		t.Errorf("trigger = %v, want the CLI with no actor", trigger)
	}
	if ci != nil {
		t.Errorf("ci = %v, want none", ci)
	}
}

func TestARunInGitHubActionsIsTriggeredByCIAndNamesTheRun(t *testing.T) {
	t.Parallel()
	getenv := environment(map[string]string{
		"CI":                "true",
		"GITHUB_ACTIONS":    "true",
		"GITHUB_ACTOR":      "octocat",
		"GITHUB_REPOSITORY": "acme/shop",
		"GITHUB_SERVER_URL": "https://github.com",
		"GITHUB_RUN_ID":     "9876543210",
	})

	trigger, ci := ReadTrigger(getenv, "12")

	if trigger.GetKind() != consolev1.TriggerKind_TRIGGER_KIND_CI || trigger.GetActor() != "octocat" {
		t.Errorf("trigger = %v, want CI by octocat", trigger)
	}
	want := &consolev1.CI{Name: "github-actions", Repo: "acme/shop", RunUrl: "https://github.com/acme/shop/actions/runs/9876543210", Pr: 12}
	if ci.GetName() != want.GetName() || ci.GetRepo() != want.GetRepo() || ci.GetRunUrl() != want.GetRunUrl() || ci.GetPr() != want.GetPr() {
		t.Errorf("ci = %v, want %v", ci, want)
	}
	if err := protovalidate.Validate(ci); err != nil {
		t.Errorf("ci is one the console refuses: %v", err)
	}
}

func TestACIWithoutAKnownProviderIsNamedCI(t *testing.T) {
	t.Parallel()

	trigger, ci := ReadTrigger(environment(map[string]string{"CI": "true"}), "")

	if trigger.GetKind() != consolev1.TriggerKind_TRIGGER_KIND_CI {
		t.Errorf("trigger kind = %v, want CI", trigger.GetKind())
	}
	if ci.GetName() != "ci" {
		t.Errorf("ci name = %q, want ci", ci.GetName())
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestTheSourceOfACommittedCheckoutNamesItsCommitAndBranch(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "first")
	head := gitIn(t, dir, "rev-parse", "HEAD")

	source := ReadSource(dir, environment(nil))

	if source.GetCommit()+"\n" != head || source.GetBranch() != "main" || source.GetDirty() {
		t.Errorf("source = %v, want commit %q on main, clean", source, head)
	}
	if err := protovalidate.Validate(source); err != nil {
		t.Errorf("source is one the console refuses: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ReadSource(dir, environment(nil)).GetDirty() {
		t.Error("dirty = false after an edit, want true")
	}
}

func TestTheSourceOfADetachedCheckoutNamesTheBranchGitHubRanItFor(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, dir, "checkout", "-q", "--detach")

	source := ReadSource(dir, environment(map[string]string{"GITHUB_HEAD_REF": "feature"}))

	if source.GetBranch() != "feature" {
		t.Errorf("branch = %q, want feature", source.GetBranch())
	}
}

func TestTheSourceOfADirectoryOutsideGitIsNone(t *testing.T) {
	t.Parallel()

	if source := ReadSource(t.TempDir(), environment(nil)); source != nil {
		t.Errorf("source = %v, want none", source)
	}
}
