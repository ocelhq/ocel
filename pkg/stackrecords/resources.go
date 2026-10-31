package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

const resourcesSegment = "resources"

type Resources struct {
	Held    []provider.PhysicalResource `json:"held,omitempty"`
	Workers []provider.PhysicalResource `json:"workers,omitempty"`
}

func resourcesKey(tier environment.Tier, slug string, stack naming.StackName) records.Key {
	return StacksPartition(tier, slug).Key(resourcesSegment, stack.String())
}

func WriteResources(ctx context.Context, store records.Store, ref provider.StackRef, held Resources) error {
	name := resourcesKey(ref.Tier, ref.Project, ref.Name)
	row, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	if row.Value, err = json.Marshal(held); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := store.Write(ctx, row); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func ReadResources(ctx context.Context, store records.Store, req provider.StackResources) ([]provider.PhysicalResource, error) {
	name := resourcesKey(req.Ref.Tier, req.Ref.Project, req.Ref.Name)
	row, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(row.Value) == 0 {
		return nil, nil
	}
	var held Resources
	if err := json.Unmarshal(row.Value, &held); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if !req.NewestRelease {
		return held.Held, nil
	}
	return slices.Concat(held.Held, held.Workers), nil
}

func ForgetResources(ctx context.Context, store records.Store, tier environment.Tier, slug string, stack naming.StackName) error {
	return records.Forget(ctx, store, resourcesKey(tier, slug, stack))
}
