package cloudflare

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func (p *cloudflare) refuseBootstrapBehind(ctx context.Context, accountID string, tier environment.Tier) error {
	planned, err := bootstrapWorkers(p.namespace, tier)
	if err != nil {
		return err
	}
	var phrases []string
	for _, b := range planned {
		state, err := p.readWorkerState(ctx, accountID, b)
		if err != nil {
			return fmt.Errorf("read %s: %w", b.what, err)
		}
		switch {
		case !state.present:
			phrases = append(phrases, fmt.Sprintf("the %s %q is missing", b.what, b.scriptName))
		case !state.upToDate():
			phrases = append(phrases, fmt.Sprintf("the %s %q is behind this build", b.what, b.scriptName))
		}
	}
	if len(phrases) == 0 {
		return nil
	}
	joined := strings.Join(phrases, " and ")
	return refusal.Refuse(refusal.CodeNotReady,
		"%s%s, and the entry worker this deploy uploads calls it, so serving it would fail every request it routes.\nRe-run `%s` to install this build's, then deploy again",
		strings.ToUpper(joined[:1]), joined[1:], provider.BootstrapCommand(tier))
}
