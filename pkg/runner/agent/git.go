package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/tracing"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func clone(ctx context.Context, git, dir, home string, source *runnerv1.GitSource) error {
	if !strings.HasPrefix(source.GetCloneUrl(), "https://") {
		return errors.New("the job's git source is not cloned over https")
	}
	if !commitPattern.MatchString(source.GetCommit()) {
		return fmt.Errorf("the job's git source names %q, which is no full commit id", source.GetCommit())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	credential := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + source.GetToken()))
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ALLOW_PROTOCOL=https",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + credential,
	}
	run := func(args ...string) error {
		cmd := tracing.CommandContext(ctx, git, args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, strings.ReplaceAll(strings.TrimSpace(string(out)), source.GetToken(), "[token]"))
		}
		return nil
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"fetch", "--quiet", "--depth", "1", "--no-tags", source.GetCloneUrl(), source.GetCommit()},
		{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
	} {
		if err := run(args...); err != nil {
			return fmt.Errorf("clone the source at %s: %w", source.GetCommit(), err)
		}
	}
	head, err := tracing.CommandContext(ctx, git, "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("read the cloned commit: %w", err)
	}
	if got := strings.TrimSpace(string(head)); got != source.GetCommit() {
		return fmt.Errorf("the clone checked out %s, not %s", got, source.GetCommit())
	}
	return nil
}
