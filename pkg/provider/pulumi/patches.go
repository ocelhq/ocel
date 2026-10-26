package pulumi

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type ResourceRef struct {
	Token string
	Name  string
}

type Patches struct {
	patches map[ResourceRef]map[string]any
	sites   map[ResourceRef]string

	mu      sync.Mutex
	claimed map[ResourceRef]bool
}

func NewPatches() *Patches {
	return &Patches{patches: map[ResourceRef]map[string]any{}, sites: map[ResourceRef]string{}}
}

func (p *Patches) Add(ref ResourceRef, site string, patch map[string]any) error {
	claimed := patch
	if earlier, taken := p.patches[ref]; taken {
		merged, err := mergeClaims(earlier, patch, p.sites[ref], site)
		if err != nil {
			return err
		}
		claimed = merged
	}
	p.patches[ref] = claimed
	p.sites[ref] = site
	return nil
}

func (p *Patches) For(ref ResourceRef) (map[string]any, bool) {
	if p == nil {
		return nil, false
	}
	patch, patched := p.patches[ref]
	return patch, patched
}

func (p *Patches) IsEmpty() bool { return p == nil || len(p.patches) == 0 }

func (p *Patches) Len() int {
	if p == nil {
		return 0
	}
	return len(p.patches)
}

func (p *Patches) Install(pctx *sdk.Context) error {
	if p.IsEmpty() {
		return nil
	}
	p.mu.Lock()
	p.claimed = make(map[ResourceRef]bool, len(p.patches))
	p.mu.Unlock()
	return pctx.RegisterResourceTransform(func(_ context.Context, args *sdk.ResourceTransformArgs) *sdk.ResourceTransformResult {
		merged, claimed := p.Claim(ResourceRef{Token: args.Type, Name: args.Name}, args.Props)
		if !claimed {
			return nil
		}
		return &sdk.ResourceTransformResult{Props: merged, Opts: args.Opts}
	})
}

func (p *Patches) Claim(ref ResourceRef, props sdk.Map) (sdk.Map, bool) {
	p.mu.Lock()
	patch, patched := p.patches[ref]
	if patched {
		if p.claimed == nil {
			p.claimed = map[ResourceRef]bool{}
		}
		p.claimed[ref] = true
	}
	p.mu.Unlock()
	if !patched {
		return nil, false
	}
	return mergeProps(props, patch), true
}

func (p *Patches) RefuseUnclaimed() error {
	if p.IsEmpty() {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var missed []string
	for ref := range p.patches {
		if !p.claimed[ref] {
			missed = append(missed, p.sites[ref])
		}
	}
	if len(missed) == 0 {
		return nil
	}
	slices.Sort(missed)
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform patches %s, and this deploy provisioned nothing to apply it to; a patch that reaches no resource is a patch this deploy never honoured",
		strings.Join(missed, ", "))
}

func mergeProps(props sdk.Map, patch map[string]any) sdk.Map {
	merged := sdk.Map{}
	maps.Copy(merged, props)
	for _, field := range slices.Sorted(maps.Keys(patch)) {
		merged[field] = mergeInput(merged[field], patch[field])
	}
	return merged
}

func mergeInput(current sdk.Input, over any) sdk.Input {
	nested, isMap := over.(map[string]any)
	if !isMap {
		return sdk.Any(over)
	}
	under, layered := current.(sdk.Map)
	if !layered {
		return sdk.Any(over)
	}
	return mergeProps(under, nested)
}

func mergeClaims(earlier, over map[string]any, at, also string) (map[string]any, error) {
	merged := map[string]any{}
	maps.Copy(merged, earlier)
	for _, field := range slices.Sorted(maps.Keys(over)) {
		value := over[field]
		if seen, taken := merged[field]; taken && !sameValue(seen, value) {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"%s and %s are the same underlying resource, and the transforms give its %s two different values; ocel shares it between them, so patch it once",
				at, also, field)
		}
		merged[field] = value
	}
	return merged, nil
}

func sameValue(a, b any) bool {
	switch left := a.(type) {
	case map[string]any:
		right, ok := b.(map[string]any)
		return ok && maps.EqualFunc(left, right, sameValue)
	case []any:
		right, ok := b.([]any)
		return ok && slices.EqualFunc(left, right, sameValue)
	}
	return a == b
}
