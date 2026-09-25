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

func Publish(ctx context.Context, store Store, at Coordinate, records []Record) error {
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

func Prune(ctx context.Context, store Store, at Coordinate, kept []Record) error {
	held, err := store.ListBindings(ctx, &envvarsv1.ListBindingsRequest{
		Slug:        at.Slug,
		Tier:        at.Tier,
		Environment: at.environment(),
	})
	if err != nil {
		return fmt.Errorf("read the records inline bindings keep: %w", err)
	}
	for _, record := range held.GetBindings() {
		if record.GetOwner() != naming.InlineRecordOwner || !naming.IsInlineRecord(record.GetName()) {
			continue
		}
		if slices.ContainsFunc(kept, func(r Record) bool { return r.Binding.GetName() == record.GetName() }) {
			continue
		}
		if _, err := store.RemoveBinding(ctx, &envvarsv1.RemoveBindingRequest{
			Slug:        at.Slug,
			Tier:        at.Tier,
			Environment: at.environment(),
			Name:        record.GetName(),
		}); err != nil {
			return fmt.Errorf("remove %s, which no inline binding keeps any more: %w", record.GetName(), err)
		}
	}
	return nil
}
