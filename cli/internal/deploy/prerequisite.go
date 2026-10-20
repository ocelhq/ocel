package deploy

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func ensureProject(ctx context.Context, dependencies Dependencies, command, cwd string, loaded *project.Project, yes, dry bool, stdout io.Writer, stdin io.Reader) (consent.Policy, *project.Project, error) {
	policy := consent.NewPolicy(command, yes, dependencies.CanAsk(stdin), stdout, stdin)
	policy.DryRun = dry
	if err := policy.Refuse(); err != nil {
		return policy, nil, err
	}
	if loaded != nil {
		return policy, loaded, nil
	}
	cfg, err := dependencies.EnsureProject(ctx, cwd, policy)
	if prerequisite.IsDeclined(err) {
		return policy, nil, nil
	}
	return policy, cfg, err
}
