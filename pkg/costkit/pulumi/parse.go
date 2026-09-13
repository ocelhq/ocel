package pulumi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

const (
	SourcePulumi = "pulumi"
	SourceSST    = "sst"

	KindStack = "stack"
	KindStage = "stage"
)

type Options struct {
	Source string
	Name   string
}

func (o Options) kind() string {
	if o.Source == SourceSST {
		return KindStage
	}
	return KindStack
}

type state struct {
	URN      string         `json:"urn"`
	Type     string         `json:"type"`
	Custom   bool           `json:"custom"`
	Parent   string         `json:"parent"`
	Provider string         `json:"provider"`
	Inputs   map[string]any `json:"inputs"`
}

type record struct {
	op string
	state
}

type envelope struct {
	Steps []struct {
		Op       string `json:"op"`
		URN      string `json:"urn"`
		NewState *state `json:"newState"`
		OldState *state `json:"oldState"`
		New      *state `json:"new"`
		Old      *state `json:"old"`
	} `json:"steps"`
	Config     map[string]any `json:"config"`
	Deployment *struct {
		Resources []state `json:"resources"`
	} `json:"deployment"`
}

func Parse(raw []byte, opts Options) (*costv1.ResourceSet, error) {
	records, config, err := read(raw)
	if err != nil {
		return nil, err
	}
	return build(records, config, opts)
}

func Merge(state, diff []byte, opts Options) (*costv1.ResourceSet, error) {
	base, config, err := read(state)
	if err != nil {
		return nil, err
	}
	changed, _, err := read(diff)
	if err != nil {
		return nil, err
	}
	order := make([]string, 0, len(base)+len(changed))
	byURN := make(map[string]record, len(base)+len(changed))
	for _, held := range base {
		if _, seen := byURN[held.URN]; !seen {
			order = append(order, held.URN)
		}
		byURN[held.URN] = held
	}
	for _, held := range changed {
		if held.op == opDelete {
			delete(byURN, held.URN)
			continue
		}
		if _, seen := byURN[held.URN]; !seen {
			order = append(order, held.URN)
		}
		byURN[held.URN] = held
	}
	records := make([]record, 0, len(order))
	for _, urn := range order {
		if held, kept := byURN[urn]; kept {
			records = append(records, held)
		}
	}
	return build(records, config, opts)
}

func read(raw []byte) ([]record, map[string]any, error) {
	var steps []struct {
		Op  string `json:"op"`
		URN string `json:"urn"`
		New *state `json:"new"`
		Old *state `json:"old"`
	}
	if err := json.Unmarshal(raw, &steps); err == nil {
		records := make([]record, 0, len(steps))
		for _, step := range steps {
			held := step.New
			if held == nil {
				held = step.Old
			}
			if held == nil {
				continue
			}
			records = append(records, record{op: step.Op, state: *held})
		}
		return records, nil, nil
	}
	var held envelope
	if err := json.Unmarshal(raw, &held); err != nil {
		return nil, nil, fmt.Errorf("this is none of a pulumi preview, an sst diff or a stack export: %w", err)
	}
	switch {
	case held.Deployment != nil:
		records := make([]record, 0, len(held.Deployment.Resources))
		for _, resource := range held.Deployment.Resources {
			records = append(records, record{state: resource})
		}
		return records, nil, nil
	case held.Steps != nil:
		records := make([]record, 0, len(held.Steps))
		for _, step := range held.Steps {
			carried := step.NewState
			if step.Op == opDelete {
				carried = step.OldState
			}
			if carried == nil {
				continue
			}
			records = append(records, record{op: step.Op, state: *carried})
		}
		return records, held.Config, nil
	}
	return nil, nil, errors.New("this is none of a pulumi preview, an sst diff or a stack export")
}

const opDelete = "delete"

const (
	typeStack        = "pulumi:pulumi:Stack"
	providerTypeHead = "pulumi:providers:"
)

const kindComponent = "component"

func build(records []record, config map[string]any, opts Options) (*costv1.ResourceSet, error) {
	tree := &costkit.Tree{}
	name := opts.Name
	if name == "" && len(records) > 0 {
		name = urnStack(records[0].URN)
	}
	root := tree.Scope("", opts.kind(), name)
	byURN := make(map[string]record, len(records))
	for _, held := range records {
		if held.op != opDelete {
			byURN[held.URN] = held
		}
	}
	regions := providerRegions(records)
	scopes := map[string]string{}
	var scopeOf func(urn string, walked []string) (string, error)
	scopeOf = func(urn string, walked []string) (string, error) {
		held, known := byURN[urn]
		if !known || held.Type == typeStack {
			return root, nil
		}
		if id, built := scopes[urn]; built {
			return id, nil
		}
		if slices.Contains(walked, urn) {
			return "", fmt.Errorf("this names a parent chain that never reaches the stack: %s", strings.Join(append(walked, urn), " is parented by "))
		}
		walked = append(walked, urn)
		if held.Custom {
			return scopeOf(held.Parent, walked)
		}
		parent, err := scopeOf(held.Parent, walked)
		if err != nil {
			return "", err
		}
		id := tree.Scope(parent, kindComponent, urnName(urn))
		scopes[urn] = id
		return id, nil
	}
	for _, held := range records {
		if held.op == opDelete || !held.Custom || held.Type == typeStack || strings.HasPrefix(held.Type, providerTypeHead) {
			continue
		}
		vendor, tf, ok := Token(held.Type)
		if !ok {
			continue
		}
		pkg, _, _ := strings.Cut(held.Type, ":")
		region, known := regions[providerURN(held.Provider)]
		if !known {
			region = configRegion(config, pkg)
		}
		props, unknown := properties(held.Inputs)
		scope, err := scopeOf(held.Parent, nil)
		if err != nil {
			return nil, err
		}
		resource := tree.Add(scope, vendor, tf, urnName(held.URN), region, props, unknown...)
		resource.Id = held.URN
		resource.Tags = tags(held.Inputs)
	}
	return tree.Set(opts.Source)
}

func urnStack(urn string) string {
	rest, held := strings.CutPrefix(urn, "urn:pulumi:")
	if !held {
		return ""
	}
	stack, _, _ := strings.Cut(rest, "::")
	return stack
}

func urnName(urn string) string {
	if at := strings.LastIndex(urn, "::"); at >= 0 {
		return urn[at+2:]
	}
	return urn
}
