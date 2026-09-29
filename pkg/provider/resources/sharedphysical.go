package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

const sharedPhysicalAttempts = 8

type sharedPhysical struct {
	Revisions map[string]string `json:"revisions"`
}

type heldRemoval[T any] struct {
	remove          func(context.Context, provider.StackRef, []T, progress.Progress) error
	removeRevisions func(context.Context, provider.StackRef, []T, progress.Progress) error
	held            func(T) (physical, revision string)
}

func sharedPhysicalKey(ref provider.StackRef, physical string) keyvalue.Key {
	return keyvalue.Partition{Tier: ref.Tier, Root: keyvalue.RootSharedPhysicals, Path: []string{ref.Project}}.Key(physical)
}

func heldFunction(function provider.Function) (string, string) {
	return function.Physical, function.Revision
}

func heldContainer(container provider.AppContainer) (string, string) {
	return container.Physical, container.Revision
}

func recordHeld[T any](ctx context.Context, f *hookStacks, ref provider.StackRef, provisioned []T, held func(T) (physical, revision string)) error {
	for _, each := range provisioned {
		physical, revision := held(each)
		if physical == "" {
			continue
		}
		if _, err := f.changeHolders(ctx, ref, physical, func(revisions map[string]string) {
			revisions[ref.Name.String()] = revision
		}); err != nil {
			return err
		}
	}
	return nil
}

func (f *hookStacks) removeHolder(ctx context.Context, ref provider.StackRef, physical string) (map[string]string, error) {
	return f.changeHolders(ctx, ref, physical, func(revisions map[string]string) {
		delete(revisions, ref.Name.String())
	})
}

func (f *hookStacks) changeHolders(ctx context.Context, ref provider.StackRef, physical string, change func(map[string]string)) (map[string]string, error) {
	key := sharedPhysicalKey(ref, physical)
	for range sharedPhysicalAttempts {
		stored, err := keyvalue.ReadOrEmpty(ctx, f.keyValues, key)
		if err != nil {
			return nil, fmt.Errorf("read the releases holding %s: %w", physical, err)
		}
		holders := sharedPhysical{Revisions: map[string]string{}}
		if len(stored.Value) > 0 {
			if err := json.Unmarshal(stored.Value, &holders); err != nil {
				return nil, fmt.Errorf("decode the releases holding %s: %w", physical, err)
			}
		}
		if holders.Revisions == nil {
			holders.Revisions = map[string]string{}
		}
		change(holders.Revisions)
		err = f.writeHolders(ctx, stored, holders)
		if errors.Is(err, keyvalue.ErrStale) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("record the releases holding %s: %w", physical, err)
		}
		return holders.Revisions, nil
	}
	return nil, fmt.Errorf("record the releases holding %s: it moved under %d attempts", physical, sharedPhysicalAttempts)
}

func (f *hookStacks) writeHolders(ctx context.Context, stored keyvalue.Entry, holders sharedPhysical) error {
	if len(holders.Revisions) > 0 {
		var err error
		if stored.Value, err = json.Marshal(holders); err != nil {
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

func removeHeld[T any](ctx context.Context, f *hookStacks, ref provider.StackRef, going []T, removal heldRemoval[T], progress progress.Progress) error {
	if removal.removeRevisions == nil {
		return removeAll(ctx, ref, going, removal.remove, progress)
	}
	var whole, revisions []T
	for _, each := range going {
		physical, revision := removal.held(each)
		if physical == "" {
			whole = append(whole, each)
			continue
		}
		others, err := f.removeHolder(ctx, ref, physical)
		if err != nil {
			return err
		}
		switch {
		case len(others) == 0:
			whole = append(whole, each)
		case revision != "" && !slices.Contains(slices.Collect(maps.Values(others)), revision):
			revisions = append(revisions, each)
		}
	}
	if err := removeAll(ctx, ref, whole, removal.remove, progress); err != nil {
		return err
	}
	return removeAll(ctx, ref, revisions, removal.removeRevisions, progress)
}
