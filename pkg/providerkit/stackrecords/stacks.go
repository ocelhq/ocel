package stackrecords

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

type Stack struct {
	Kind      provider.StackKind  `json:"kind"`
	App       string              `json:"app,omitempty"`
	Release   string              `json:"release,omitempty"`
	Build     string              `json:"identity,omitempty"`
	Bindings  []provider.Binding  `json:"bindings,omitempty"`
	Functions []provider.Function `json:"functions,omitempty"`

	Containers []provider.AppContainer `json:"containers,omitempty"`

	WrittenBy provider.WrittenBy `json:"writer,omitempty"`
	UpdatedAt int64              `json:"updated_at,omitempty"`
}

type NamedStack struct {
	Name naming.StackName
	Stack
}

func Read(ctx context.Context, store records.Store, class edge.Class, slug string, stack naming.StackName) (Stack, bool, error) {
	name := StackRecord(class, slug, stack)
	row, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return Stack{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	if len(row.Bytes) == 0 {
		return Stack{}, false, nil
	}
	var recorded Stack
	if err := json.Unmarshal(row.Bytes, &recorded); err != nil {
		return Stack{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	return recorded, true, nil
}

func Write(ctx context.Context, store records.Store, class edge.Class, slug string, stack naming.StackName, recorded Stack) error {
	name := StackRecord(class, slug, stack)
	row, err := records.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	recorded.UpdatedAt = time.Now().Unix()
	if row.Bytes, err = json.Marshal(recorded); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := store.Write(ctx, row); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func Forget(ctx context.Context, store records.Store, class edge.Class, slug string, stack naming.StackName) error {
	return records.Forget(ctx, store, StackRecord(class, slug, stack))
}

func List(ctx context.Context, records records.Store, class edge.Class, slug string) ([]NamedStack, error) {
	under := StacksRecord(class, slug)
	recorded, err := records.List(ctx, under)
	if err != nil {
		return nil, fmt.Errorf("read %s's stacks: %w", slug, err)
	}
	entries := make([]NamedStack, 0, len(recorded))
	for _, record := range recorded {
		rest, named := record.Name.Under(under)
		if !named {
			continue
		}
		name, err := naming.ParseStackName(rest[0])
		if err != nil {
			continue
		}
		entry := NamedStack{Name: name}
		if len(record.Bytes) > 0 {
			if err := json.Unmarshal(record.Bytes, &entry.Stack); err != nil {
				return nil, fmt.Errorf("read %s: %w", record.Name, err)
			}
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b NamedStack) int {
		return cmp.Compare(a.Name.String(), b.Name.String())
	})
	return entries, nil
}
