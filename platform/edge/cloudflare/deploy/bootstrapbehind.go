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

func (p *cloudflare) listWorkersBehind(ctx context.Context, accountID string, names originNames, tier environment.Tier) ([]string, error) {
	planned, err := names.bootstrapWorkers(tier, p.keptBucketName(names, tier))
	if err != nil {
		return nil, err
	}
	var phrases []string
	for _, worker := range planned {
		state, err := p.readWorkerState(ctx, accountID, worker)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", worker.what, err)
		}
		switch {
		case !state.present:
			phrases = append(phrases, fmt.Sprintf("the %s %q is missing", worker.what, worker.scriptName))
		case !state.upToDate():
			phrases = append(phrases, fmt.Sprintf("the %s %q is behind this build", worker.what, worker.scriptName))
		}
	}
	return phrases, nil
}

func (p *cloudflare) forgetStoreVersion(ctx context.Context, tier environment.Tier) error {
	if !p.KeepsObjects || p.ForgetStoreVersion == nil {
		return nil
	}
	return p.ForgetStoreVersion(ctx, tier)
}

func (p *cloudflare) refuseBootstrapBehind(behind []string, tier environment.Tier, caller bootstrapCaller) error {
	joined := strings.Join(behind, " and ")
	joined = strings.ToUpper(joined[:1]) + joined[1:]
	if p.KeepsObjects {
		return refusal.Refuse(refusal.CodeNotReady, "%s\n%s%s to install this build's",
			joined, strings.ToUpper(caller.retry[:1]), caller.retry[1:])
	}
	calls := "it"
	if len(behind) > 1 {
		calls = "them"
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s, and %s calls %s, so serving it would fail every request it routes.\nRe-run `%s` to install this build's, then %s",
		joined, caller.worker, calls, provider.BootstrapCommand(tier), caller.retry)
}
