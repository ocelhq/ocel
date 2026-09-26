package providerkit

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type RecordedStack struct {
	Kind      provider.StackKind  `json:"kind"`
	App       string              `json:"app,omitempty"`
	Release   string              `json:"release,omitempty"`
	Identity  string              `json:"identity,omitempty"`
	Bindings  []provider.Binding  `json:"bindings,omitempty"`
	Functions []provider.Function `json:"functions,omitempty"`

	Containers []provider.AppContainer `json:"containers,omitempty"`

	WrittenBy provider.WrittenBy `json:"writer,omitempty"`
	UpdatedAt int64              `json:"updated_at,omitempty"`
}

type StackEntry struct {
	Name naming.StackName
	RecordedStack
}

func ReadStack(ctx context.Context, store records.Store, class edge.Class, slug string, stack naming.StackName) (RecordedStack, bool, error) {
	name := StackRecord(class, slug, stack)
	held, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return RecordedStack{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	if len(held.Bytes) == 0 {
		return RecordedStack{}, false, nil
	}
	var recorded RecordedStack
	if err := json.Unmarshal(held.Bytes, &recorded); err != nil {
		return RecordedStack{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	return recorded, true, nil
}

func WriteStack(ctx context.Context, store records.Store, class edge.Class, slug string, stack naming.StackName, recorded RecordedStack) error {
	name := StackRecord(class, slug, stack)
	held, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	recorded.UpdatedAt = time.Now().Unix()
	if held.Bytes, err = json.Marshal(recorded); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := store.Write(ctx, held); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func ForgetStack(ctx context.Context, store records.Store, class edge.Class, slug string, stack naming.StackName) error {
	return records.Forget(ctx, store, StackRecord(class, slug, stack))
}

func ReadStacks(ctx context.Context, records records.Store, class edge.Class, slug string) ([]StackEntry, error) {
	under := StacksRecord(class, slug)
	held, err := records.List(ctx, under)
	if err != nil {
		return nil, fmt.Errorf("read %s's stacks: %w", slug, err)
	}
	entries := make([]StackEntry, 0, len(held))
	for _, record := range held {
		rest, named := record.Name.Under(under)
		if !named {
			continue
		}
		name, err := naming.ParseStackName(rest[0])
		if err != nil {
			continue
		}
		entry := StackEntry{Name: name}
		if len(record.Bytes) > 0 {
			if err := json.Unmarshal(record.Bytes, &entry.RecordedStack); err != nil {
				return nil, fmt.Errorf("read %s: %w", record.Name, err)
			}
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b StackEntry) int {
		return cmp.Compare(a.Name.String(), b.Name.String())
	})
	return entries, nil
}
