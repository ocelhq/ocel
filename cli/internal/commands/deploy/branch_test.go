package deploy

import (
	"os/exec"
	"strings"
	"testing"
)

func createDetachedCheckout(t *testing.T) string {
	t.Helper()
	dir := createAttachedCheckout(t, "main")
	if out, err := exec.Command("git", "-C", dir, "checkout", "--quiet", "--detach").CombinedOutput(); err != nil {
		t.Fatalf("git checkout --detach: %v\n%s", err, out)
	}
	return dir
}

func createAttachedCheckout(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch", branch},
		{"-c", "user.name=ocel", "-c", "user.email=ocel@example.com", "commit", "--quiet", "--allow-empty", "--message", "root"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestAnAttachedCheckoutReadsItsOwnBranchOverGitHubHeadRef(t *testing.T) {
	dir := createAttachedCheckout(t, "feature/checkout")
	t.Setenv("GITHUB_HEAD_REF", "feature/login")

	branch, err := ReadGitBranch(dir)
	if err != nil {
		t.Fatalf("ReadGitBranch: %v", err)
	}
	if branch != "feature/checkout" {
		t.Errorf("ReadGitBranch() = %q, want %q", branch, "feature/checkout")
	}
}

func TestADetachedCheckoutReadsItsBranchFromGitHubHeadRef(t *testing.T) {
	dir := createDetachedCheckout(t)
	t.Setenv("GITHUB_HEAD_REF", "feature/login")

	branch, err := ReadGitBranch(dir)
	if err != nil {
		t.Fatalf("ReadGitBranch: %v", err)
	}
	if branch != "feature/login" {
		t.Errorf("ReadGitBranch() = %q, want %q", branch, "feature/login")
	}
}

func TestADetachedCheckoutWithoutGitHubHeadRefIsRefusedAndAsksForAName(t *testing.T) {
	dir := createDetachedCheckout(t)
	t.Setenv("GITHUB_HEAD_REF", "")

	branch, err := ReadGitBranch(dir)
	if err == nil {
		t.Fatalf("ReadGitBranch() = %q, want an error", branch)
	}
	if !strings.Contains(err.Error(), "ocel preview up <name>") {
		t.Errorf("error %q does not ask for a name as `ocel preview up <name>`", err)
	}
}
