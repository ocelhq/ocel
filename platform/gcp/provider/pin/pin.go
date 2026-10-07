package pin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

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

	Close(ctx context.Context, service string, stillActive router.StillActive) error

	ReadRollback(ctx context.Context, service, revision string) (Rollback, bool, error)

	RecordRollback(ctx context.Context, service, revision string, rollback Rollback) error

	Warm(ctx context.Context, service, revision, path string) error
}

type Rollback struct {
	Previous string `json:"previous,omitempty"`
	Opened   bool   `json:"opened,omitempty"`
}

type Tags map[string]string

func ReadTags(ctx context.Context, pins Pins, record router.ReleaseRecord) (Tags, error) {
	if record.Revisions[ResolveService(record)] == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"release %s of %s recorded no revision of the service it answers on, and a deployment is answered on the tags of the revisions its deploy created: "+
				"re-deploy %s so its release records one",
			record.Release, record.App, record.App)
	}
	tags := Tags{}
	for _, service := range slices.Sorted(maps.Keys(record.Revisions)) {
		tag, err := pins.ReadTag(ctx, service, record.Revisions[service])
		if err != nil {
			return nil, err
		}
		tags[service] = tag
	}
	return tags, nil
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
		if err := pins.Close(ctx, service, nil); err != nil {
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
		if err := pins.Close(ctx, service, nil); err != nil {
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
	var pinning []router.ReleaseRecord
	for _, app := range slices.Sorted(maps.Keys(move.Records)) {
		record := move.Records[app]
		if len(record.Revisions) == 0 {
			return router.Unserved{Err: refusal.Refuse(refusal.CodeInvalid,
				"release %s of %s recorded no revision, and a promotion on Cloud Run is a traffic pin onto the revision the release deployed: "+
					"re-deploy %s so its release records one, then promote that",
				record.Release, app, app)}
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

func WarmRevisions(ctx context.Context, warm func(ctx context.Context, service, revision, path string) error, records map[string]router.ReleaseRecord, progress progress.Log) {
	var (
		waiting sync.WaitGroup
		mu      sync.Mutex
		failed  []string
	)
	for _, record := range records {
		for service, revision := range record.Revisions {
			path := "/"
			if service == ResolveService(record) && record.HealthPath != "" {
				path = record.HealthPath
			}
			waiting.Go(func() {
				if err := warm(ctx, service, revision, path); err != nil {
					mu.Lock()
					failed = append(failed, err.Error())
					mu.Unlock()
				}
			})
		}
	}
	waiting.Wait()
	if progress == nil {
		return
	}
	for _, failure := range slices.Sorted(slices.Values(failed)) {
		progress.Warn("Could not warm a promoted revision, so its first visitor waits for it to start: " + failure)
	}
}

var ErrRevisionServed = errors.New("the service serves this revision again")

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
		rollback, err := readRollback(ctx, pins, each)
		if err != nil {
			left = append(left, each.service+" serves "+each.revision)
			continue
		}
		if serving, err := pins.ReadServing(ctx, each.service); err == nil && serving != each.revision {
			err := pins.RecordRollback(ctx, each.service, each.revision, rollback)
			if err == nil {
				continue
			}
			if !errors.Is(err, ErrRevisionServed) {
				left = append(left, formatPendingRollback(each))
				continue
			}
		}
		closed := false
		if rollback.Opened {
			recorded, err := putBackOrRecordRollback(ctx, pins, each, rollback, func() error {
				return pins.Close(ctx, each.service, stillPinned(pins, each))
			})
			if errors.Is(err, errNotRecorded) {
				left = append(left, formatPendingRollback(each))
				continue
			}
			if recorded {
				continue
			}
			if err != nil {
				left = append(left, each.service+" answers everyone on "+each.revision)
				continue
			}
			closed = true
		}
		switch rollback.Previous {
		case each.revision:
		case "":
			if !rollback.Opened {
				left = append(left, each.service+" serves "+each.revision)
			}
		default:
			owed := rollback
			if closed {
				owed.Opened = false
			}
			_, err := putBackOrRecordRollback(ctx, pins, each, owed, func() error {
				return pins.Restore(ctx, each.service, rollback.Previous, stillPinned(pins, each))
			})
			if errors.Is(err, errNotRecorded) {
				left = append(left, formatPendingRollback(each))
			} else if err != nil {
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

const repinAttempts = 3

var errNotRecorded = errors.New("the rollback this promotion owes could not be recorded")

func formatPendingRollback(each pinned) string {
	return each.service + " is put back on " + each.revision + " if the promotion that pinned it since fails"
}

func putBackOrRecordRollback(ctx context.Context, pins Pins, each pinned, rollback Rollback, put func() error) (recorded bool, err error) {
	for range repinAttempts {
		err = put()
		if !errors.Is(err, errRepinned) {
			return false, err
		}
		err = pins.RecordRollback(ctx, each.service, each.revision, rollback)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, ErrRevisionServed) {
			return false, fmt.Errorf("%w: %w", errNotRecorded, err)
		}
	}
	return false, err
}

func readRollback(ctx context.Context, pins Pins, each pinned) (Rollback, error) {
	rollback := Rollback{Previous: each.previous, Opened: each.opened}
	if each.previous == "" || each.previous == each.revision {
		return rollback, nil
	}
	earlier, found, err := pins.ReadRollback(ctx, each.service, each.previous)
	if err != nil || !found {
		return rollback, err
	}
	return Rollback{Previous: earlier.Previous, Opened: rollback.Opened || earlier.Opened}, nil
}

func ResolveService(record router.ReleaseRecord) string {
	if record.Physical != "" {
		return record.Physical
	}
	return record.EntryFunction
}
