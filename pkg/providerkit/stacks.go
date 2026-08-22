package providerkit

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
)

type Stack struct {
	Kind      StackKind  `json:"kind"`
	App       string     `json:"app,omitempty"`
	Release   string     `json:"release,omitempty"`
	Identity  string     `json:"identity,omitempty"`
	Links     []Link     `json:"links,omitempty"`
	Functions []Function `json:"functions,omitempty"`
	Writer    Writer     `json:"writer,omitempty"`
	UpdatedAt int64      `json:"updated_at,omitempty"`
}

type StackEntry struct {
	Name naming.StackName
	Stack
}

func ReadStack(ctx context.Context, records RecordStore, slug string, stack naming.StackName) (Stack, bool, error) {
	name := StackRecord(slug, stack)
	held, err := Held(ctx, records, name)
	if err != nil {
		return Stack{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	if len(held.Bytes) == 0 {
		return Stack{}, false, nil
	}
	var recorded Stack
	if err := json.Unmarshal(held.Bytes, &recorded); err != nil {
		return Stack{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	return recorded, true, nil
}

func WriteStack(ctx context.Context, records RecordStore, slug string, stack naming.StackName, recorded Stack) error {
	name := StackRecord(slug, stack)
	held, err := Held(ctx, records, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	recorded.UpdatedAt = time.Now().Unix()
	if held.Bytes, err = json.Marshal(recorded); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := records.Write(ctx, held); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func ForgetStack(ctx context.Context, records RecordStore, slug string, stack naming.StackName) error {
	return Forget(ctx, records, StackRecord(slug, stack))
}

func ReadStacks(ctx context.Context, records RecordStore, slug string) ([]StackEntry, error) {
	held, err := records.List(ctx, StacksRecord(slug))
	if err != nil {
		return nil, fmt.Errorf("read %s's stacks: %w", slug, err)
	}
	entries := make([]StackEntry, 0, len(held))
	for _, record := range held {
		name, err := naming.ParseStackName(record.Name[len(record.Name)-1])
		if err != nil {
			continue
		}
		entry := StackEntry{Name: name}
		if len(record.Bytes) > 0 {
			if err := json.Unmarshal(record.Bytes, &entry.Stack); err != nil {
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
