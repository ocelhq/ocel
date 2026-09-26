package inlinebinding

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

type Store interface {
	SetBinding(context.Context, *envvarsv1.SetBindingRequest) (*envvarsv1.SetBindingResponse, error)
	RemoveBinding(context.Context, *envvarsv1.RemoveBindingRequest) (*envvarsv1.RemoveBindingResponse, error)
	ListBindings(context.Context, *envvarsv1.ListBindingsRequest) (*envvarsv1.ListBindingsResponse, error)
}

type Coordinate struct {
	Slug        string
	Tier        environmentv1.Tier
	Environment string
}

func (c Coordinate) environment() string {
	if c.Tier != environmentv1.Tier_TIER_PREVIEW {
		return ""
	}
	return c.Environment
}

func Deploy(ctx context.Context, store Store, at Coordinate, records []Record, deploy func() error) error {
	before, err := publishedBindings(ctx, store, at)
	if err != nil {
		return err
	}
	if err := publish(ctx, store, at, records); err != nil {
		return err
	}
	if err := deploy(); err != nil {
		return err
	}
	after, err := publishedBindings(ctx, store, at)
	if err != nil {
		return err
	}
	for name, version := range after {
		if before[name] != version || slices.ContainsFunc(records, func(r Record) bool { return r.Binding.GetName() == name }) {
			continue
		}
		if _, err := store.RemoveBinding(ctx, &envvarsv1.RemoveBindingRequest{
			Slug:        at.Slug,
			Tier:        at.Tier,
			Environment: at.environment(),
			Name:        name,
		}); err != nil {
			return fmt.Errorf("remove %s, which no inline binding keeps any more: %w", name, err)
		}
	}
	return nil
}

func publish(ctx context.Context, store Store, at Coordinate, records []Record) error {
	for _, r := range records {
		if _, err := store.SetBinding(ctx, &envvarsv1.SetBindingRequest{
			Slug:        at.Slug,
			Tier:        at.Tier,
			Environment: at.environment(),
			Binding:     r.Binding,
			Owner:       naming.InlineRecordOwner,
		}); err != nil {
			return fmt.Errorf("keep the record `%s` binds: %w", r.Site, err)
		}
	}
	return nil
}

func publishedBindings(ctx context.Context, store Store, at Coordinate) (map[string]uint64, error) {
	listed, err := store.ListBindings(ctx, &envvarsv1.ListBindingsRequest{
		Slug:        at.Slug,
		Tier:        at.Tier,
		Environment: at.environment(),
	})
	if err != nil {
		return nil, fmt.Errorf("read the records inline bindings keep: %w", err)
	}
	versions := map[string]uint64{}
	for _, record := range listed.GetBindings() {
		if record.GetOwner() == naming.InlineRecordOwner && naming.IsInlineRecord(record.GetName()) {
			versions[record.GetName()] = record.GetVersion()
		}
	}
	return versions, nil
}
