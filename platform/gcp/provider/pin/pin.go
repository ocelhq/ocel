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

func ReadTags(ctx context.Context, pins Pins, record router.DeploymentRecord) (Tags, error) {
	if record.Revisions[record.Physical] == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"build %s of %s recorded no revision of the service it answers on, and a deployment is answered on the tags of the revisions its deploy created: "+
				"re-deploy %s so its release records one",
			record.Build, record.App, record.App)
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

func WarmRevisions(ctx context.Context, warm func(ctx context.Context, service, revision, path string) error, records map[string]router.DeploymentRecord, progress progress.Log) {
	var (
		waiting sync.WaitGroup
		mu      sync.Mutex
		failed  []string
	)
	for _, record := range records {
		for service, revision := range record.Revisions {
			path := "/"
			if service == record.Physical && record.HealthPath != "" {
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
			if recorded := recordRollback(ctx, pins, each, rollback, &left); recorded {
				continue
			}
		}
		if rollback.Opened {
			err := pins.Close(ctx, each.service, stillPinned(pins, each))
			if errors.Is(err, errRepinned) {
				if recorded := recordRollback(ctx, pins, each, rollback, &left); recorded {
					continue
				}
				err = pins.Close(ctx, each.service, stillPinned(pins, each))
			}
			if err != nil && !errors.Is(err, errRepinned) {
				left = append(left, each.service+" answers everyone on "+each.revision)
				continue
			}
		}
		switch rollback.Previous {
		case each.revision:
		case "":
			if !rollback.Opened {
				left = append(left, each.service+" serves "+each.revision)
			}
		default:
			err := pins.Restore(ctx, each.service, rollback.Previous, stillPinned(pins, each))
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

func recordRollback(ctx context.Context, pins Pins, each pinned, rollback Rollback, left *[]string) bool {
	err := pins.RecordRollback(ctx, each.service, each.revision, rollback)
	if errors.Is(err, ErrRevisionServed) {
		return false
	}
	if err != nil {
		*left = append(*left, each.service+" is put back on "+each.revision+" if the promotion that pinned it since fails")
	}
	return true
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
