package pin

import (
	"context"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

type Pins interface {
	Pin(ctx context.Context, service, revision string, stillActive router.StillActive) error
}

func MovePointer(ctx context.Context, pins Pins, move router.PointerMove, progress progress.Log) error {
	var pinning []router.DeploymentRecord
	for _, app := range slices.Sorted(maps.Keys(move.Records)) {
		record := move.Records[app]
		if len(record.Revisions) == 0 {
			return router.Unserved{Err: refusal.Refuse(refusal.CodeInvalid,
				"build %s of %s recorded no revision, and a promotion on Cloud Run is a traffic pin onto the revision the build deployed: "+
					"re-deploy %s so its release records one, then promote that",
				record.Build, app, app)}
		}
		pinning = append(pinning, record)
	}
	if err := move.RefuseInactive(ctx); err != nil {
		return err
	}
	pinned := false
	for _, record := range pinning {
		for _, service := range slices.Sorted(maps.Keys(record.Revisions)) {
			revision := record.Revisions[service]
			if progress != nil {
				progress.Say("Pinning all of " + record.App + "'s traffic to revision " + revision + " of Cloud Run service " + service)
			}
			if err := pins.Pin(ctx, service, revision, move.StillActive); err != nil {
				if !pinned {
					return router.Unserved{Err: err}
				}
				return err
			}
			pinned = true
		}
	}
	return nil
}
