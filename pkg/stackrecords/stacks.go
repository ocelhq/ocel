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

const (
	PropertyDeclared       = "declared"
	PropertyTopicSpec      = "spec"
	PropertySweepUploads   = "sweepUploads"
	PropertyAllowedOrigins = "allowedOrigins"
)

type Stack struct {
	Kind         provider.StackKind  `json:"kind"`
	App          string              `json:"app,omitempty"`
	ReleaseToken string              `json:"releaseToken,omitempty"`
	Release      string              `json:"release,omitempty"`
	Bindings     []provider.Binding  `json:"bindings,omitempty"`
	Functions    []provider.Function `json:"functions,omitempty"`

	Containers []provider.AppContainer `json:"containers,omitempty"`
	Image      string                  `json:"image,omitempty"`

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

func (s Stack) Images() []string {
	images := []string{}
	add := func(image string) {
		if image != "" && !slices.Contains(images, image) {
			images = append(images, image)
		}
	}
	add(s.Image)
	for _, container := range s.Containers {
		add(container.Image)
	}
	return images
}

func Write(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string, stack naming.StackName, recorded Stack) error {
	name := StackKey(tier, slug, stack)
	row, err := keyvalue.ReadOrEmpty(ctx, store, name)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	recorded.Bindings = slices.Clone(recorded.Bindings)
	for i, binding := range recorded.Bindings {
		recorded.Bindings[i] = keepRecordedProperties(binding)
	}
	recorded.UpdatedAt = time.Now().Unix()
	if row.Value, err = json.Marshal(recorded); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if stack.IsInfra() {
		if _, err := store.Write(ctx, row); err != nil {
			return fmt.Errorf("record %s: %w", name, err)
		}
		return nil
	}
	listed, err := keyvalue.ReadOrEmpty(ctx, store, appImagesKey(tier, slug, stack))
	if err != nil {
		return fmt.Errorf("read %s: %w", listed.Key, err)
	}
	if listed.Value, err = json.Marshal(recorded.Images()); err != nil {
		return fmt.Errorf("record %s: %w", listed.Key, err)
	}
	if err := store.WritePair(ctx, row, listed); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	return nil
}

var recordOnlyProperties = map[provider.BindingType][]string{
	provider.BindingTopic:  {PropertyDeclared, PropertyTopicSpec},
	provider.BindingTask:   {PropertyDeclared, PropertyTopicSpec},
	provider.BindingBucket: {PropertySweepUploads, PropertyAllowedOrigins},
}

func keepRecordedProperties(binding provider.Binding) provider.Binding {
	if len(binding.Properties) == 0 {
		return binding
	}
	kept := ListRecordedProperties(binding.Type)
	binding.Properties = maps.Clone(binding.Properties)
	maps.DeleteFunc(binding.Properties, func(name, _ string) bool { return !slices.Contains(kept, name) })
	return binding
}

func ListRecordedProperties(t provider.BindingType) []string {
	kept := slices.Clone(recordOnlyProperties[t])
	field := (&bindingsv1.Binding{}).ProtoReflect().Descriptor().Oneofs().ByName("properties").Fields().ByName(protoreflect.Name(t))
	if t == provider.BindingCustom || field == nil || field.Message() == nil {
		return kept
	}
	properties := field.Message().Fields()
	for i := range properties.Len() {
		if options, _ := properties.Get(i).Options().(*descriptorpb.FieldOptions); !options.GetDebugRedact() {
			kept = append(kept, properties.Get(i).JSONName())
		}
	}
	return kept
}

func Forget(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug string, stack naming.StackName) error {
	if !stack.IsInfra() {
		if err := keyvalue.Forget(ctx, store, appImagesKey(tier, slug, stack)); err != nil {
			return err
		}
	}
	return keyvalue.Forget(ctx, store, StackKey(tier, slug, stack))
}

func listAppImages(ctx context.Context, store keyvalue.Store, tier environment.Tier, slug, app string) (map[naming.StackName][]string, error) {
	listed, err := store.List(ctx, StacksPartition(tier, slug), appImagesSegment, app)
	if err != nil {
		return nil, fmt.Errorf("read the images %s's %s stacks record in %s: %w", slug, app, tier, err)
	}
	recorded := make(map[naming.StackName][]string, len(listed))
	for _, entry := range listed {
		if len(entry.Key.Path) != 3 {
			continue
		}
		name, err := naming.ParseStackName(entry.Key.Path[2])
		if err != nil {
			continue
		}
		var images []string
		if err := json.Unmarshal(entry.Value, &images); err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Key, err)
		}
		recorded[name] = images
	}
	return recorded, nil
}

func ListRecordedAppImages(ctx context.Context, store keyvalue.Store, slug, app string, except ...provider.StackRef) (map[string]bool, error) {
	recorded := map[string]bool{}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		stacks, err := listAppImages(ctx, store, tier, slug, app)
		if err != nil {
			return nil, err
		}
		for name, images := range stacks {
			if slices.ContainsFunc(except, func(ref provider.StackRef) bool { return ref.Tier == tier && ref.Name == name }) {
				continue
			}
			for _, image := range images {
				recorded[image] = true
			}
		}
	}
	return recorded, nil
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
