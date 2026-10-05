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
	Pin(ctx context.Context, service, revision string, stillActive router.StillActive) (bool, error)

	Restore(ctx context.Context, service, revision string, stillActive router.StillActive) error

	ReadServing(ctx context.Context, service string) (string, error)

	ReadTag(ctx context.Context, service, revision string) (string, error)

	Untag(ctx context.Context, service, tag string) error

	Close(ctx context.Context, service string) error
}

type Pointers map[string][]string

func ReplacePointer(ctx context.Context, pins Pins, pointers Pointers, move router.PointerMove) (Pointers, error) {
	if _, _, deployment := router.ParseDeploymentPointer(move.Pointer); deployment {
		return pointers, nil
	}
	var services []string
	for _, record := range move.Records {
		services = append(services, slices.Collect(maps.Keys(record.Revisions))...)
	}
	pointer := router.ResolvePointer(move.Pointer)
	recorded := maps.Clone(pointers)
	if recorded == nil {
		recorded = Pointers{}
	}
	recorded[pointer] = slices.Sorted(slices.Values(services))
	var errs []error
	for _, service := range pointers[pointer] {
		if recorded.pins(service) {
			continue
		}
		if err := pins.Close(ctx, service); err != nil {
			recorded[pointer] = append(recorded[pointer], service)
			errs = append(errs, err)
		}
	}
	return recorded, errors.Join(errs...)
}

func ClosePointer(ctx context.Context, pins Pins, pointers Pointers, pointer string) (Pointers, error) {
	pointer = router.ResolvePointer(pointer)
	services, recorded := pointers[pointer]
	if !recorded {
		return pointers, nil
	}
	kept := maps.Clone(pointers)
	delete(kept, pointer)
	for _, service := range services {
		if kept.pins(service) {
			continue
		}
		if err := pins.Close(ctx, service); err != nil {
			return pointers, err
		}
	}
	if len(kept) == 0 {
		kept = nil
	}
	return kept, nil
}

func (p Pointers) pins(service string) bool {
	for _, services := range p {
		if slices.Contains(services, service) {
			return true
		}
	}
	return false
}

type pinned struct {
	service  string
	revision string
	previous string
	opened   bool
}

func MovePointer(ctx context.Context, pins Pins, pointers Pointers, move router.PointerMove, progress progress.Log) (Pointers, error) {
	if err := pinRecords(ctx, pins, move, progress); err != nil {
		return pointers, err
	}
	return ReplacePointer(ctx, pins, pointers, move)
}

func pinRecords(ctx context.Context, pins Pins, move router.PointerMove, progress progress.Log) error {
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
			opened, err := pins.Pin(ctx, service, revision, move.StillActive)
			moved = append(moved, pinned{service: service, revision: revision, previous: previous, opened: opened})
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
		if serving, err := pins.ReadServing(ctx, each.service); err == nil && serving != each.revision {
			continue
		}
		if each.opened {
			if err := pins.Close(ctx, each.service); err != nil {
				left = append(left, each.service+" answers everyone on "+each.revision)
				continue
			}
		}
		switch each.previous {
		case each.revision:
		case "":
			if !each.opened {
				left = append(left, each.service+" serves "+each.revision)
			}
		default:
			err := pins.Restore(ctx, each.service, each.previous, stillPinned(pins, each))
			if err != nil && !errors.Is(err, errRepinned) {
				left = append(left, each.service+" serves "+each.revision)
			}
		}
	}
	if len(left) == 0 {
		return router.Unserved{Err: cause}
	}
	return fmt.Errorf("%w; the services this promotion had already pinned could not all be put back, so %s, while every other service serves what it served before",
		cause, strings.Join(left, ", "))
}
