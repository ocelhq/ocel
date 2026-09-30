package deploy

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const detachedHead = "HEAD"

func ReadGitBranch(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("determine current git branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", errors.New("determine current git branch: empty ref")
	}
	if branch != detachedHead {
		return branch, nil
	}
	if headRef := os.Getenv("GITHUB_HEAD_REF"); headRef != "" {
		return headRef, nil
	}
	return "", errors.New("HEAD is detached and GITHUB_HEAD_REF is unset, so no branch names this preview: name it, as in `ocel preview up <name>`")
}
