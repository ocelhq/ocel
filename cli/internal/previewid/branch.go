package previewid

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ocelhq/ocel/pkg/tracing"
)

const detachedHeadExit = 1

func ReadGitBranch(ctx context.Context, dir string) (string, error) {
	out, err := tracing.Command(ctx, "git", "-C", dir, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == detachedHeadExit:
		return readDetachedBranch()
	case errors.As(err, &exit) && strings.Contains(string(exit.Stderr), "not a git repository"):
		return "", fmt.Errorf("%s is not in a git checkout, so no branch names this preview: name it, as in `ocel preview up <name>`", dir)
	case errors.As(err, &exit):
		return "", fmt.Errorf("determine current git branch: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	case err != nil:
		return "", fmt.Errorf("determine current git branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", errors.New("determine current git branch: empty ref")
	}
	return branch, nil
}

func readDetachedBranch() (string, error) {
	if headRef := os.Getenv("GITHUB_HEAD_REF"); headRef != "" {
		return headRef, nil
	}
	return "", errors.New("HEAD is detached and GITHUB_HEAD_REF is unset, so no branch names this preview: name it, as in `ocel preview up <name>`")
}
