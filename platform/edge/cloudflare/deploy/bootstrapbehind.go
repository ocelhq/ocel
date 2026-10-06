package cloudflare

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type bootstrapCaller struct {
	worker string
	retry  string
}

func (p *cloudflare) refuseBootstrapBehind(ctx context.Context, accountID string, tier environment.Tier, caller bootstrapCaller) error {
	planned, err := bootstrapWorkers(p.namespace, tier)
	if err != nil {
		return err
	}
	var phrases []string
	for _, worker := range planned {
		state, err := p.readWorkerState(ctx, accountID, worker)
		if err != nil {
			return fmt.Errorf("read %s: %w", worker.what, err)
		}
		switch {
		case !state.present:
			phrases = append(phrases, fmt.Sprintf("the %s %q is missing", worker.what, worker.scriptName))
		case !state.upToDate():
			phrases = append(phrases, fmt.Sprintf("the %s %q is behind this build", worker.what, worker.scriptName))
		}
	}
	if len(phrases) == 0 {
		return nil
	}
	calls := "it"
	if len(phrases) > 1 {
		calls = "them"
	}
	joined := strings.Join(phrases, " and ")
	return refusal.Refuse(refusal.CodeNotReady,
		"%s%s, and %s calls %s, so serving it would fail every request it routes.\nRe-run `%s` to install this build's, then %s",
		strings.ToUpper(joined[:1]), joined[1:], caller.worker, calls, provider.BootstrapCommand(tier), caller.retry)
}
