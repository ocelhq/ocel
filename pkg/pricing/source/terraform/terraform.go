package terraform

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

const (
	Source = "terraform"

	KindModule = "module"
	RootScope  = "root"

	modeManaged  = "managed"
	actionDelete = "delete"
	actionForget = "forget"
)

type change struct {
	Address       string `json:"address"`
	ModuleAddress string `json:"module_address"`
	Mode          string `json:"mode"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	Deposed       string `json:"deposed"`
	Change        struct {
		Actions        []string `json:"actions"`
		After          any      `json:"after"`
		AfterUnknown   any      `json:"after_unknown"`
		AfterSensitive any      `json:"after_sensitive"`
	} `json:"change"`
}

type stateResource struct {
	Address         string `json:"address"`
	Mode            string `json:"mode"`
	Type            string `json:"type"`
	Name            string `json:"name"`
	Values          any    `json:"values"`
	SensitiveValues any    `json:"sensitive_values"`
}

type module struct {
	Address      string          `json:"address"`
	Resources    []stateResource `json:"resources"`
	ChildModules []module        `json:"child_modules"`
}

type stateValues struct {
	Values *struct {
		RootModule module `json:"root_module"`
	} `json:"values"`
}

type resource struct {
	address   string
	module    string
	typ       string
	name      string
	values    any
	unknown   any
	sensitive any
	config    configured
}

func ReadPlan(raw []byte) (set, prior *costv1.ResourceSet, err error) {
	var plan struct {
		ResourceChanges []change            `json:"resource_changes"`
		PriorState      *stateValues        `json:"prior_state"`
		Configuration   configuration       `json:"configuration"`
		Variables       map[string]variable `json:"variables"`
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, nil, fmt.Errorf("terraform plan: %w", err)
	}
	providers := plan.Configuration.providerKeys()
	expressions := plan.Configuration.expressions()
	var planned []resource
	for _, c := range plan.ResourceChanges {
		if c.Mode != modeManaged || c.Deposed != "" || c.Change.After == nil || isRemoval(c.Change.Actions) {
			continue
		}
		planned = append(planned, resource{
			address:   c.Address,
			module:    c.ModuleAddress,
			typ:       c.Type,
			name:      c.Name,
			values:    c.Change.After,
			unknown:   c.Change.AfterUnknown,
			sensitive: c.Change.AfterSensitive,
			config:    configurationOf(expressions, c),
		})
	}
	region := func(r resource, props properties.Properties) (string, bool) {
		return plan.Configuration.region(r, props, providers, plan.Variables)
	}
	if set, err = newSet(planned, region, plan.Configuration.references); err != nil {
		return nil, nil, err
	}
	var current []resource
	if plan.PriorState != nil {
		current = plan.PriorState.resources()
	}
	if prior, err = newSet(current, region, referencesByValue); err != nil {
		return nil, nil, err
	}
	return set, prior, nil
}

func ReadState(raw []byte) (*costv1.ResourceSet, error) {
	var state stateValues
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, fmt.Errorf("terraform state: %w", err)
	}
	return newSet(state.resources(), stateRegion, referencesByValue)
}

func configurationOf(expressions map[string]map[string]any, c change) configured {
	body, found := expressions[configAddress(moduleCalls(c.ModuleAddress), c.Type+"."+c.Name)]
	if !found {
		return unread
	}
	return configuredBody(body)
}

func isRemoval(actions []string) bool {
	return slices.Equal(actions, []string{actionDelete}) || slices.Equal(actions, []string{actionForget})
}

func (s *stateValues) resources() []resource {
	if s.Values == nil {
		return nil
	}
	var out []resource
	var walk func(m module)
	walk = func(m module) {
		for _, r := range m.Resources {
			if r.Mode != modeManaged {
				continue
			}
			out = append(out, resource{
				address:   r.Address,
				module:    m.Address,
				typ:       r.Type,
				name:      r.Name,
				values:    r.Values,
				sensitive: r.SensitiveValues,
			})
		}
		for _, child := range m.ChildModules {
			walk(child)
		}
	}
	walk(s.Values.RootModule)
	return out
}

type tree struct {
	scopes    []*costv1.Scope
	built     map[string]bool
	resources []*costv1.Resource
}

func newSet(resources []resource, region regionOf, references referencesOf) (*costv1.ResourceSet, error) {
	t := &tree{
		scopes: []*costv1.Scope{{Id: RootScope, Kind: KindModule, Name: RootScope}},
		built:  map[string]bool{RootScope: true},
	}
	props := make([]properties.Properties, len(resources))
	for i, r := range resources {
		props[i] = readProperties(r.values, r.unknown, r.sensitive, r.config)
	}
	referenced := references(resources, props)
	for i, r := range resources {
		resolved, known := region(r, props[i])
		built, err := properties.Resource{
			ID:          r.address,
			Scope:       t.scopeOf(r.module),
			Vendor:      properties.TypeVendor(r.typ),
			Type:        r.typ,
			Name:        localName(r),
			Region:      resolved,
			RegionKnown: known,
			Properties:  props[i],
			References:  referenced[i],
		}.Proto()
		if err != nil {
			return nil, err
		}
		t.resources = append(t.resources, built)
	}
	properties.DropUnresolvedReferences(t.resources)
	return &costv1.ResourceSet{Source: Source, Scopes: t.scopes, Resources: t.resources}, nil
}

func (t *tree) scopeOf(address string) string {
	if address == "" {
		return RootScope
	}
	if t.built[address] {
		return address
	}
	parent := t.scopeOf(parentModule(address))
	t.built[address] = true
	t.scopes = append(t.scopes, &costv1.Scope{Id: address, Parent: parent, Kind: KindModule, Name: address})
	return address
}

func localAddress(r resource) string {
	if r.module == "" {
		return r.address
	}
	return strings.TrimPrefix(r.address, r.module+".")
}

func localName(r resource) string {
	return strings.TrimPrefix(localAddress(r), r.typ+".")
}
