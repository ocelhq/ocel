package stackrecords

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Stack struct {
	Kind         provider.StackKind  `json:"kind"`
	App          string              `json:"app,omitempty"`
	ReleaseToken string              `json:"releaseToken,omitempty"`
	Release      string              `json:"release,omitempty"`
	Bindings     []provider.Binding  `json:"bindings,omitempty"`
	Functions    []provider.Function `json:"functions,omitempty"`

	Containers []provider.AppContainer `json:"containers,omitempty"`

	Resources      []byte `json:"resources,omitempty"`
	ResourceDigest string `json:"resource_digest,omitempty"`

	WrittenBy provider.WrittenBy `json:"writer,omitempty"`
	UpdatedAt int64              `json:"updated_at,omitempty"`
}

type NamedStack struct {
	Name naming.StackName
	Stack
}

func Read(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string, stack naming.StackName) (Stack, bool, error) {
	name := StackKey(tier, slug, stack)
	row, err := keyvalue.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return Stack{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	if len(row.Value) == 0 {
		return Stack{}, false, nil
	}
	var recorded Stack
	if err := json.Unmarshal(row.Value, &recorded); err != nil {
		return Stack{}, false, fmt.Errorf("read %s: %w", name, err)
	}
	return recorded, true, nil
}

func Write(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string, stack naming.StackName, recorded Stack) error {
	name := StackKey(tier, slug, stack)
	row, err := keyvalue.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	recorded.Bindings = slices.Clone(recorded.Bindings)
	for i, binding := range recorded.Bindings {
		recorded.Bindings[i] = redactSecrets(binding)
	}
	recorded.UpdatedAt = time.Now().Unix()
	if row.Value, err = json.Marshal(recorded); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if _, err := store.Write(ctx, row); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

func redactSecrets(binding provider.Binding) provider.Binding {
	if len(binding.Properties) == 0 {
		return binding
	}
	field := (&bindingsv1.Binding{}).ProtoReflect().Descriptor().Oneofs().ByName("properties").Fields().ByName(protoreflect.Name(binding.Type))
	if binding.Type == provider.BindingCustom || field == nil {
		binding.Properties = nil
		return binding
	}
	binding.Properties = maps.Clone(binding.Properties)
	properties := field.Message().Fields()
	for i := range properties.Len() {
		if options, _ := properties.Get(i).Options().(*descriptorpb.FieldOptions); options.GetDebugRedact() {
			delete(binding.Properties, properties.Get(i).JSONName())
		}
	}
	return binding
}

func Forget(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string, stack naming.StackName) error {
	return keyvalue.Forget(ctx, store, StackKey(tier, slug, stack))
}

func List(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string) ([]NamedStack, error) {
	recorded, err := store.List(ctx, StacksPartition(tier, slug))
	if err != nil {
		return nil, fmt.Errorf("read %s's stacks: %w", slug, err)
	}
	stacks := make([]NamedStack, 0, len(recorded))
	for _, entry := range recorded {
		name, err := naming.ParseStackName(entry.Key.Path[0])
		if err != nil {
			continue
		}
		stack := NamedStack{Name: name}
		if len(entry.Value) > 0 {
			if err := json.Unmarshal(entry.Value, &stack.Stack); err != nil {
				return nil, fmt.Errorf("read %s: %w", entry.Key, err)
			}
		}
		stacks = append(stacks, stack)
	}
	slices.SortFunc(stacks, func(a, b NamedStack) int {
		return cmp.Compare(a.Name.String(), b.Name.String())
	})
	return stacks, nil
}
