package providerserver

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func (r *deployRun) recordFronting(ctx context.Context) error {
	if len(r.spec.Apps) == 0 {
		return nil
	}
	fronts := slices.ContainsFunc(r.spec.Apps, func(entry provider.AppEntry) bool { return r.isFronted(entry.App) })
	if fronts == r.state.Fronts {
		return nil
	}
	pointer, err := r.ledger.Read(ctx, r.spec.Pointer)
	if err != nil {
		return err
	}
	r.state.Fronts, r.state.FrontingChangedAfter = fronts, pointer.Sequence
	return nil
}

func (s *edgeSession) frontsApps() bool {
	for _, kind := range s.state.Apps {
		if s.isFrontedRouter(kind) {
			return true
		}
	}
	return false
}

func (s *edgeSession) refuseRollbackAcrossFronting(current ledger.Pointer, target router.Promotion) error {
	front := describeFront(s.front.Kind())
	if s.frontsApps() != s.state.Fronts {
		now := "no longer fronts"
		if !s.state.Fronts {
			now = "now fronts"
		}
		return refusal.Refuse(refusal.CodeNotReady, "%s %s this project's apps since its last deploy\nDeploy before rolling back", front, now)
	}
	at := slices.IndexFunc(current.Promotions, func(recorded ledger.RecordedPromotion) bool { return recorded.PromotionID == target.PromotionID })
	if at >= 0 && current.Promotions[at].Sequence > s.state.FrontingChangedAfter {
		return nil
	}
	change := "stopped fronting"
	if s.state.Fronts {
		change = "started fronting"
	}
	return refusal.Refuse(refusal.CodeNotReady, "promotion %s was deployed before %s %s this project's apps\nRoll back to a promotion deployed since",
		target.PromotionID, front, change)
}
