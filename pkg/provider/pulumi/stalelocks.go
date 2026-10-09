package pulumi

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func runReleasingStaleLock[T any](ctx context.Context, a *Automation, setup WorkspaceSpec, progress progress.Log, run func() (T, error)) (T, error) {
	result, err := run()
	if !isLocked(err) || !provider.HasLease(ctx, setup.Ref) {
		return result, err
	}
	if err := a.engine().Unlock(ctx, setup); err != nil {
		var none T
		return none, fmt.Errorf("release the lock an interrupted run left on stack %s: %w", setup.Stack, err)
	}
	if progress != nil {
		progress.Say("Released the lock an interrupted run left on stack " + setup.Stack + ": this run holds the lease over its environment, so no other run can be working on it")
	}
	return run()
}
