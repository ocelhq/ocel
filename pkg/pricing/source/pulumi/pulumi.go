package pulumi

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

const (
	SourcePulumi = "pulumi"
	SourceSST    = "sst"

	KindStack     = "stack"
	KindStage     = "stage"
	KindComponent = "component"
)

const (
	typeStack       = "pulumi:pulumi:Stack"
	opSame          = "same"
	providerTypes   = "pulumi:providers:"
	pulumiTypes     = "pulumi:"
	sstTypes        = "sst:"
	zoneProperty    = "zone"
	urnPrefix       = "urn:pulumi:"
	urnSeparator    = "::"
	urnSegmentCount = 4
)

type resourceState struct {
	URN      string         `json:"urn"`
	Type     string         `json:"type"`
	Custom   bool           `json:"custom"`
	Delete   bool           `json:"delete"`
	External bool           `json:"external"`
	Parent   string         `json:"parent"`
	Provider string         `json:"provider"`
	Inputs   map[string]any `json:"inputs"`
	Outputs  map[string]any `json:"outputs"`

	PropertyDependencies map[string][]string `json:"propertyDependencies"`
}

type step struct {
	Op       string         `json:"op"`
	NewState *resourceState `json:"newState"`
	OldState *resourceState `json:"oldState"`
	New      *resourceState `json:"new"`
	Old      *resourceState `json:"old"`
}

var (
	opsWithoutNewState = []string{"delete", "delete-replaced", "discard", "discard-replaced", "remove-pending-replace", "read-discard"}
	opsOfReads         = []string{"read", "read-replacement", "read-discard"}
)

const (
	AssumptionChangesOnly = "the preview lists only resources it changes; run `pulumi preview --json --show-sames` to price the unchanged ones too"
	AssumptionSSTDiff     = "sst diff --json lists only changes, so it prices the change, not the whole stage; compare it with --diff"
)

func ReadPreview(raw []byte) (set, prior *costv1.ResourceSet, assumptions []string, err error) {
	var preview struct {
		Config map[string]any `json:"config"`
		Steps  []step         `json:"steps"`
	}
	if err := json.Unmarshal(raw, &preview); err != nil {
		return nil, nil, nil, fmt.Errorf("pulumi preview: %w", err)
	}
	if set, prior, err = readSteps(preview.Steps, preview.Config, false); err != nil {
		return nil, nil, nil, err
	}
	if omitsUnchanged(preview.Steps) {
		assumptions = append(assumptions, AssumptionChangesOnly)
	}
	return set, prior, assumptions, nil
}

func ReadSteps(raw []byte) (set, prior *costv1.ResourceSet, assumptions []string, err error) {
	var steps []step
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, nil, nil, fmt.Errorf("sst diff: %w", err)
	}
	if set, prior, err = readSteps(steps, nil, true); err != nil {
		return nil, nil, nil, err
	}
	return set, prior, []string{AssumptionSSTDiff}, nil
}

func omitsUnchanged(steps []step) bool {
	stackExists := false
	for _, s := range steps {
		if s.Op == opSame && s.NewState != nil && s.NewState.Type != typeStack {
			return false
		}
		if s.OldState != nil {
			stackExists = true
		}
	}
	return stackExists
}

func ReadExport(raw []byte) (*costv1.ResourceSet, error) {
	var export struct {
		Deployment struct {
			Resources []*resourceState `json:"resources"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal(raw, &export); err != nil {
		return nil, fmt.Errorf("pulumi stack export: %w", err)
	}
	var recorded []*resourceState
	for _, held := range export.Deployment.Resources {
		if held != nil && !held.Delete && !held.External {
			recorded = append(recorded, held)
		}
	}
	doc := newDocument(recorded, nil)
	return doc.set(recorded)
}

func readSteps(steps []step, config map[string]any, fromSST bool) (set, prior *costv1.ResourceSet, err error) {
	var planned, current, all []*resourceState
	for _, s := range steps {
		if slices.Contains(opsOfReads, s.Op) {
			continue
		}
		next, previous := s.NewState, s.OldState
		if next == nil && previous == nil {
			next, previous = s.New, s.Old
		}
		if next != nil && !slices.Contains(opsWithoutNewState, s.Op) {
			planned = append(planned, next)
			all = append(all, next)
		}
		if previous != nil {
			current = append(current, previous)
			all = append(all, previous)
		}
	}
	doc := newDocument(all, config)
	if fromSST {
		doc.source = SourceSST
	}
	if set, err = doc.set(planned); err != nil {
		return nil, nil, err
	}
	if prior, err = doc.set(current); err != nil {
		return nil, nil, err
	}
	return set, prior, nil
}

type document struct {
	source    string
	stack     string
	config    map[string]any
	byURN     map[string]*resourceState
	providers map[string]properties.Properties
}

func newDocument(records []*resourceState, config map[string]any) *document {
	doc := &document{
		source:    SourcePulumi,
		config:    config,
		byURN:     map[string]*resourceState{},
		providers: map[string]properties.Properties{},
	}
	for _, held := range records {
		if doc.stack == "" {
			doc.stack = urnStack(held.URN)
		}
		if strings.HasPrefix(held.Type, sstTypes) {
			doc.source = SourceSST
		}
		if strings.HasPrefix(held.Type, providerTypes) {
			if _, seen := doc.providers[held.URN]; !seen {
				doc.providers[held.URN] = readProperties(held.Inputs, held.Outputs, nil)
			}
			continue
		}
		if _, seen := doc.byURN[held.URN]; !seen {
			doc.byURN[held.URN] = held
		}
	}
	return doc
}

func (d *document) rootKind() string {
	if d.source == SourceSST {
		return KindStage
	}
	return KindStack
}

type tree struct {
	doc       *document
	root      string
	scopes    []*costv1.Scope
	scopeIDs  map[string]string
	resources []*costv1.Resource
	seen      map[string]bool
}

func (d *document) set(records []*resourceState) (*costv1.ResourceSet, error) {
	kind := d.rootKind()
	t := &tree{
		doc:      d,
		root:     kind + ":" + d.stack,
		scopeIDs: map[string]string{},
		seen:     map[string]bool{},
	}
	t.scopes = append(t.scopes, &costv1.Scope{Id: t.root, Kind: kind, Name: d.stack})
	for _, held := range records {
		if err := t.add(held); err != nil {
			return nil, err
		}
	}
	properties.DropUnresolvedReferences(t.resources)
	return &costv1.ResourceSet{Source: d.source, Scopes: t.scopes, Resources: t.resources}, nil
}

func (d *document) references(held *resourceState, names fieldNames) map[string]string {
	out := map[string]string{}
	for input, dependencies := range held.PropertyDependencies {
		var custom []string
		for _, urn := range dependencies {
			if dependency, known := d.byURN[urn]; (!known || dependency.Custom) && !slices.Contains(custom, urn) {
				custom = append(custom, urn)
			}
		}
		if len(custom) == 1 {
			name, _ := names.terraformName(input)
			out[name] = custom[0]
		}
	}
	return out
}

func (t *tree) add(held *resourceState) error {
	if !held.Custom || strings.HasPrefix(held.Type, pulumiTypes) || t.seen[held.URN] {
		return nil
	}
	vendor, typ, ok := terraformType(held.Type)
	if !ok {
		return nil
	}
	t.seen[held.URN] = true
	scope, err := t.scopeOf(held.Parent, nil)
	if err != nil {
		return err
	}
	names := bridged[typ].fields
	props := readProperties(held.Inputs, held.Outputs, names)
	region, known := t.doc.region(held, vendor, props)
	resource, err := properties.Resource{
		ID:          held.URN,
		Scope:       scope,
		Vendor:      vendor,
		Type:        typ,
		Name:        urnName(held.URN),
		Region:      region,
		RegionKnown: known,
		Properties:  props,
		References:  t.doc.references(held, names),
	}.Proto()
	if err != nil {
		return err
	}
	t.resources = append(t.resources, resource)
	return nil
}

func (t *tree) scopeOf(parent string, walked []string) (string, error) {
	if parent == "" || urnType(parent) == typeStack {
		return t.root, nil
	}
	if id, built := t.scopeIDs[parent]; built {
		return id, nil
	}
	if slices.Contains(walked, parent) {
		return "", fmt.Errorf("this names a parent chain that never reaches the stack: %s", strings.Join(append(walked, parent), " is parented by "))
	}
	walked = append(walked, parent)
	held, known := t.doc.byURN[parent]
	if known && held.Custom {
		return t.scopeOf(held.Parent, walked)
	}
	scopeParent := t.root
	if known {
		var err error
		if scopeParent, err = t.scopeOf(held.Parent, walked); err != nil {
			return "", err
		}
	}
	t.scopeIDs[parent] = parent
	t.scopes = append(t.scopes, &costv1.Scope{Id: parent, Parent: scopeParent, Kind: KindComponent, Name: urnName(parent)})
	return parent, nil
}

func urnSegments(urn string) []string {
	return strings.SplitN(strings.TrimPrefix(urn, urnPrefix), urnSeparator, urnSegmentCount)
}

func urnStack(urn string) string {
	if !strings.HasPrefix(urn, urnPrefix) {
		return ""
	}
	return urnSegments(urn)[0]
}

func urnType(urn string) string {
	segments := urnSegments(urn)
	if len(segments) < urnSegmentCount {
		return ""
	}
	qualified := segments[2]
	if at := strings.LastIndex(qualified, "$"); at >= 0 {
		return qualified[at+1:]
	}
	return qualified
}

func urnName(urn string) string {
	segments := urnSegments(urn)
	if len(segments) < urnSegmentCount {
		return urn
	}
	return segments[3]
}
