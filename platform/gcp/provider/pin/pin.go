package pin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

type Pins interface {
	Pin(ctx context.Context, service, revision string, stillActive router.StillActive) error

	ReadServing(ctx context.Context, service string) (string, error)

	ReadTag(ctx context.Context, service, revision string) (string, error)

	Untag(ctx context.Context, service, tag string) error
}

type pinned struct {
	service  string
	revision string
	previous string
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
	var moved []pinned
	for _, record := range pinning {
		for _, service := range slices.Sorted(maps.Keys(record.Revisions)) {
			revision := record.Revisions[service]
			previous, err := pins.ReadServing(ctx, service)
			if err != nil {
				return restore(ctx, pins, moved, err)
			}
			if progress != nil {
				progress.Say("Pinning all of " + record.App + "'s traffic to revision " + revision + " of Cloud Run service " + service)
			}
			err = pins.Pin(ctx, service, revision, move.StillActive)
			moved = append(moved, pinned{service: service, revision: revision, previous: previous})
			if err != nil {
				return restore(ctx, pins, moved, err)
			}
		}
	}
	return nil
}

var errRepinned = errors.New("another promotion pinned this service since")

func stillPinned(pins Pins, each pinned) router.StillActive {
	return func(ctx context.Context) error {
		serving, err := pins.ReadServing(ctx, each.service)
		if err != nil {
			return err
		}
		if serving != each.revision {
			return errRepinned
		}
		return nil
	}
}

func restore(ctx context.Context, pins Pins, moved []pinned, cause error) error {
	var left []string
	for _, each := range slices.Backward(moved) {
		if each.previous == each.revision {
			continue
		}
		if serving, err := pins.ReadServing(ctx, each.service); err == nil && serving != each.revision {
			continue
		}
		if each.previous == "" {
			left = append(left, each.service+" serves "+each.revision)
			continue
		}
		err := pins.Pin(ctx, each.service, each.previous, stillPinned(pins, each))
		if err != nil && !errors.Is(err, errRepinned) {
			left = append(left, each.service+" serves "+each.revision)
		}
	}
	if len(left) == 0 {
		return router.Unserved{Err: cause}
	}
	return fmt.Errorf("%w; the services this promotion had already pinned could not all be put back, so %s, while every other service serves what it served before",
		cause, strings.Join(left, ", "))
}
