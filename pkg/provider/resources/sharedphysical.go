package resources

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const (
	sharedPhysicalAttempts = 8
	sharedRemovalWindow    = 10 * time.Minute
	sharedRemovalPoll      = 250 * time.Millisecond

	kindFunction  = "function"
	kindContainer = "container"
)

type sharedPhysical struct {
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	Holders  map[string]string `json:"holders,omitempty"`
	Kept     []string          `json:"kept,omitempty"`
	Removing *wholeRemoval     `json:"removing,omitempty"`
}

type wholeRemoval struct {
	Stack string `json:"stack"`
	Since int64  `json:"since"`
}

func (s sharedPhysical) isRemovedByAnother(stack string, now time.Time) bool {
	return s.Removing != nil && s.Removing.Stack != stack && now.Sub(time.Unix(s.Removing.Since, 0)) < sharedRemovalWindow
}

type heldItem struct {
	name     string
	physical string
	revision string
}

type compute[T any] struct {
	kind   string
	remove func(context.Context, provider.StackRef, []T, provider.ImageStore, progress.Log) error
	shared *SharedHooks[T]
	held   func(T) heldItem
	item   func(heldItem) T
	field  func(*stackrecords.Stack) *[]T
}

func functionCompute(hooks *FunctionHooks) compute[provider.Function] {
	return compute[provider.Function]{
		kind:   kindFunction,
		remove: hooks.Remove,
		shared: hooks.Shared,
		held: func(function provider.Function) heldItem {
			return heldItem{name: function.Name, physical: function.Physical, revision: function.Revision}
		},
		item: func(held heldItem) provider.Function {
			return provider.Function{Name: held.name, Physical: held.physical, Revision: held.revision}
		},
		field: func(stack *stackrecords.Stack) *[]provider.Function { return &stack.Functions },
	}
}

func containerCompute(hooks *ContainerHooks) compute[provider.AppContainer] {
	return compute[provider.AppContainer]{
		kind:   kindContainer,
		remove: hooks.Remove,
		shared: hooks.Shared,
		held: func(container provider.AppContainer) heldItem {
			return heldItem{name: container.Name, physical: container.Physical, revision: container.Revision}
		},
		item: func(held heldItem) provider.AppContainer {
			return provider.AppContainer{Name: held.name, Physical: held.physical, Revision: held.revision}
		},
		field: func(stack *stackrecords.Stack) *[]provider.AppContainer { return &stack.Containers },
	}
}

func sharedPhysicalPartition(tier environment.Tier, slug string) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootSharedPhysicals, Path: []string{slug}}
}

func provisionCompute[T any](
	ctx context.Context,
	f *hookStacks,
	spec provider.StackSpec,
	c compute[T],
	provision func(context.Context, provider.StackSpec, progress.Log) ([]T, error),
	progress progress.Log,
) ([]T, error) {
	if c.shared == nil {
		return provision(ctx, spec, progress)
	}
	planned, err := c.shared.Name(ctx, spec)
	if err != nil {
		return nil, err
	}
	if err := recordPlanned(ctx, f, spec, planned, c); err != nil {
		return nil, err
	}
	for _, each := range planned {
		if held := c.held(each); held.physical != "" {
			if err := f.claim(ctx, spec.Ref, c.kind, held); err != nil {
				return nil, err
			}
		}
	}
	provisioned, err := provision(ctx, spec, progress)
	if err != nil {
		return nil, err
	}
	stray := slices.DeleteFunc(slices.Clone(planned), func(each T) bool {
		return slices.ContainsFunc(provisioned, func(made T) bool { return c.held(made).physical == c.held(each).physical })
	})
	if err := removeCompute(ctx, f, spec.Ref, stray, c, spec.Images.Store, progress); err != nil {
		return nil, err
	}
	for _, each := range provisioned {
		held := c.held(each)
		if held.physical == "" {
			continue
		}
		retry, err := f.recordRevision(ctx, spec.Ref, c.kind, held)
		if err != nil {
			return nil, err
		}
		if err := removeRevisions(ctx, f, spec.Ref, c, held.physical, retry, spec.Images.Store, progress); err != nil && progress != nil {
			progress.Warn(fmt.Sprintf("Left revisions %v of %s in place, and the next release of it tries again: %v", retry, held.physical, err))
		}
	}
	return provisioned, nil
}

func recordPlanned[T any](ctx context.Context, f *hookStacks, spec provider.StackSpec, planned []T, c compute[T]) error {
	recorded, _, err := stackrecords.Read(ctx, f.keyValues, spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name)
	if err != nil {
		return err
	}
	if recorded.Kind == "" {
		recorded.Kind = spec.Kind
	}
	if recorded.App == "" && spec.App != nil {
		recorded.App = spec.App.App
	}
	field := c.field(&recorded)
	names := make([]string, 0, len(planned))
	for _, each := range planned {
		names = append(names, c.held(each).name)
	}
	*field = slices.DeleteFunc(*field, func(each T) bool { return !slices.Contains(names, c.held(each).name) })
	for _, each := range planned {
		if !slices.ContainsFunc(*field, func(held T) bool { return c.held(held).name == c.held(each).name }) {
			*field = append(*field, each)
		}
	}
	return stackrecords.Write(ctx, f.keyValues, spec.Ref.Tier, spec.Ref.Project, spec.Ref.Name, recorded)
}

func removeCompute[T any](ctx context.Context, f *hookStacks, ref provider.StackRef, going []T, c compute[T], images provider.ImageStore, progress progress.Log) error {
	if c.shared == nil {
		return removeAll(ctx, ref, going, c.remove, images, progress)
	}
	var batch []T
	var whole []heldItem
	var failed error
	for _, each := range going {
		held := c.held(each)
		if held.physical == "" {
			batch = append(batch, each)
			continue
		}
		released, retry, err := f.release(ctx, ref, c.kind, held)
		if err != nil {
			failed = err
			break
		}
		if released {
			batch = append(batch, each)
			whole = append(whole, held)
			continue
		}
		if err := removeRevisions(ctx, f, ref, c, held.physical, retry, images, progress); err != nil {
			failed = err
			break
		}
	}
	if len(batch) > 0 {
		removed := removeAll(ctx, ref, batch, c.remove, images, progress)
		failed = errors.Join(failed, removed)
		for _, held := range whole {
			failed = errors.Join(failed, f.finishRemoval(ctx, ref, held, removed == nil))
		}
	}
	return failed
}

func removeRevisions[T any](ctx context.Context, f *hookStacks, ref provider.StackRef, c compute[T], physical string, revisions []heldItem, images provider.ImageStore, progress progress.Log) error {
	if len(revisions) == 0 {
		return nil
	}
	going := make([]T, 0, len(revisions))
	for _, revision := range revisions {
		going = append(going, c.item(revision))
	}
	kept, err := c.shared.RemoveRevisions(ctx, ref, going, images, progress)
	if err != nil {
		return err
	}
	attempted := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		attempted = append(attempted, revision.revision)
	}
	remaining := make([]string, 0, len(kept))
	for _, each := range kept {
		remaining = append(remaining, c.held(each).revision)
	}
	return f.settleKept(ctx, ref, physical, attempted, remaining)
}

func (f *hookStacks) claim(ctx context.Context, ref provider.StackRef, kind string, held heldItem) error {
	stack := ref.Name.String()
	_, err := f.changeShared(ctx, ref, held.physical, func(entry *sharedPhysical, now time.Time) bool {
		if entry.isRemovedByAnother(stack, now) {
			return true
		}
		entry.Removing = nil
		entry.Kind, entry.Name = kind, held.name
		if _, holds := entry.Holders[stack]; !holds {
			entry.Holders[stack] = ""
		}
		return false
	})
	return err
}

func (f *hookStacks) recordRevision(ctx context.Context, ref provider.StackRef, kind string, held heldItem) ([]heldItem, error) {
	stack := ref.Name.String()
	var retry []heldItem
	_, err := f.changeShared(ctx, ref, held.physical, func(entry *sharedPhysical, _ time.Time) bool {
		entry.Kind, entry.Name = kind, held.name
		entry.Holders[stack] = held.revision
		retry = unheldKept(*entry, held.physical, "")
		return false
	})
	return retry, err
}

func (f *hookStacks) release(ctx context.Context, ref provider.StackRef, kind string, held heldItem) (bool, []heldItem, error) {
	stack := ref.Name.String()
	var whole bool
	var retry []heldItem
	_, err := f.changeShared(ctx, ref, held.physical, func(entry *sharedPhysical, now time.Time) bool {
		whole, retry = false, nil
		if entry.Kind == "" {
			entry.Kind, entry.Name = kind, held.name
		}
		delete(entry.Holders, stack)
		switch {
		case len(entry.Holders) > 0:
			retry = unheldKept(*entry, held.physical, held.revision)
		case entry.isRemovedByAnother(stack, now):
		default:
			entry.Removing = &wholeRemoval{Stack: stack, Since: now.Unix()}
			whole = true
		}
		return false
	})
	return whole, retry, err
}

func unheldKept(entry sharedPhysical, physical, own string) []heldItem {
	held := slices.Collect(maps.Values(entry.Holders))
	var retry []heldItem
	for _, revision := range append([]string{own}, entry.Kept...) {
		if revision == "" || slices.Contains(held, revision) || slices.ContainsFunc(retry, func(each heldItem) bool { return each.revision == revision }) {
			continue
		}
		retry = append(retry, heldItem{name: entry.Name, physical: physical, revision: revision})
	}
	return retry
}

func (f *hookStacks) finishRemoval(ctx context.Context, ref provider.StackRef, held heldItem, removed bool) error {
	stack := ref.Name.String()
	_, err := f.changeShared(ctx, ref, held.physical, func(entry *sharedPhysical, _ time.Time) bool {
		if entry.Removing != nil && entry.Removing.Stack == stack {
			entry.Removing = nil
		}
		if removed {
			entry.Kept = nil
			return false
		}
		entry.Holders[stack] = held.revision
		return false
	})
	return err
}

func (f *hookStacks) settleKept(ctx context.Context, ref provider.StackRef, physical string, attempted, kept []string) error {
	_, err := f.changeShared(ctx, ref, physical, func(entry *sharedPhysical, _ time.Time) bool {
		entry.Kept = slices.DeleteFunc(entry.Kept, func(revision string) bool {
			return slices.Contains(attempted, revision) && !slices.Contains(kept, revision)
		})
		for _, revision := range kept {
			if !slices.Contains(entry.Kept, revision) {
				entry.Kept = append(entry.Kept, revision)
			}
		}
		return false
	})
	return err
}

func (f *hookStacks) changeShared(ctx context.Context, ref provider.StackRef, physical string, apply func(*sharedPhysical, time.Time) bool) (sharedPhysical, error) {
	key := sharedPhysicalPartition(ref.Tier, ref.Project).Key(physical)
	for attempt := 0; attempt < sharedPhysicalAttempts; {
		stored, err := keyvalue.ReadOrEmpty(ctx, f.keyValues, key)
		if err != nil {
			return sharedPhysical{}, fmt.Errorf("read the releases holding %s: %w", physical, err)
		}
		var entry sharedPhysical
		if len(stored.Value) > 0 {
			if err := json.Unmarshal(stored.Value, &entry); err != nil {
				return sharedPhysical{}, fmt.Errorf("decode the releases holding %s: %w", physical, err)
			}
		}
		if entry.Holders == nil {
			entry.Holders = map[string]string{}
		}
		if apply(&entry, time.Now()) {
			select {
			case <-ctx.Done():
				return sharedPhysical{}, fmt.Errorf("wait for another destroy to finish removing %s: %w", physical, ctx.Err())
			case <-time.After(sharedRemovalPoll):
			}
			continue
		}
		err = f.writeShared(ctx, stored, entry)
		if errors.Is(err, keyvalue.ErrStale) {
			attempt++
			continue
		}
		if err != nil {
			return sharedPhysical{}, fmt.Errorf("record the releases holding %s: %w", physical, err)
		}
		return entry, nil
	}
	return sharedPhysical{}, fmt.Errorf("record the releases holding %s: it moved under %d attempts", physical, sharedPhysicalAttempts)
}

func (f *hookStacks) writeShared(ctx context.Context, stored keyvalue.Entry, entry sharedPhysical) error {
	if len(entry.Holders) > 0 || entry.Removing != nil {
		var err error
		if stored.Value, err = json.Marshal(entry); err != nil {
			return err
		}
		_, err = f.keyValues.Write(ctx, stored)
		return err
	}
	if len(stored.Value) == 0 {
		return nil
	}
	err := f.keyValues.Remove(ctx, stored.Key, stored.Revision)
	if errors.Is(err, keyvalue.ErrNotFound) {
		return keyvalue.ErrStale
	}
	return err
}

func RecordUnrecordedHolders(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string, within func(naming.StackName) bool) ([]naming.StackName, error) {
	entries, err := store.List(ctx, sharedPhysicalPartition(tier, slug))
	if err != nil {
		return nil, fmt.Errorf("read the physical resources %s's releases share: %w", slug, err)
	}
	unrecorded := map[naming.StackName]*stackrecords.Stack{}
	recorded := map[naming.StackName]bool{}
	for _, stored := range entries {
		var entry sharedPhysical
		if err := json.Unmarshal(stored.Value, &entry); err != nil {
			return nil, fmt.Errorf("decode %s: %w", stored.Key, err)
		}
		holders := maps.Clone(entry.Holders)
		if holders == nil {
			holders = map[string]string{}
		}
		if entry.Removing != nil {
			holders[entry.Removing.Stack] = ""
		}
		for stack, revision := range holders {
			name, err := naming.ParseStackName(stack)
			if err != nil || !within(name) || recorded[name] {
				continue
			}
			restored, seen := unrecorded[name]
			if !seen {
				_, present, err := stackrecords.Read(ctx, store, tier, slug, name)
				if err != nil {
					return nil, err
				}
				if present {
					recorded[name] = true
					continue
				}
				restored = &stackrecords.Stack{Kind: provider.StackApp, App: name.App}
				unrecorded[name] = restored
			}
			held := heldItem{name: entry.Name, physical: stored.Key.Path[0], revision: revision}
			switch entry.Kind {
			case kindFunction:
				restored.Functions = append(restored.Functions, functionCompute(&FunctionHooks{}).item(held))
			case kindContainer:
				restored.Containers = append(restored.Containers, containerCompute(&ContainerHooks{}).item(held))
			}
		}
	}
	names := slices.SortedFunc(maps.Keys(unrecorded), func(a, b naming.StackName) int { return cmp.Compare(a.String(), b.String()) })
	for _, name := range names {
		if err := stackrecords.Write(ctx, store, tier, slug, name, *unrecorded[name]); err != nil {
			return nil, err
		}
	}
	return names, nil
}
