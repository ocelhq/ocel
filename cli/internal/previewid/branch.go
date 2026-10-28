package previewid

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ocelhq/ocel/pkg/tracing"
)

const detachedHead = "HEAD"

func ReadGitBranch(ctx context.Context, dir string) (string, error) {
	out, err := tracing.Command(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
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
