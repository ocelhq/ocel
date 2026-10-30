package deploy

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
)

func ensureTarget(ctx context.Context, dependencies Dependencies, command, cwd string, yes, dry bool, stdout io.Writer, stdin io.Reader) (consent.Policy, commands.Target, error) {
	policy := dependencies.Policy(command, yes, stdout, stdin)
	policy.DryRun = dry
	target, err := dependencies.EnsureTarget(ctx, cwd, policy)
	return policy, target, err
}
