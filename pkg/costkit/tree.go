package costkit

import (
	"fmt"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

const (
	ScopeProject     = "project"
	ScopeShared      = "shared"
	ScopeEnvironment = "environment"
	ScopeApp         = "app"
)

type Tree struct {
	scopes    []*costv1.Scope
	resources []*costv1.Resource
	seen      map[string]bool
	err       error
}

func (t *Tree) Scope(parent, kind, name string) string {
	id := kind + ":" + name
	if parent != "" {
		id = parent + "/" + id
	}
	if t.seen == nil {
		t.seen = map[string]bool{}
	}
	if !t.seen[id] {
		t.seen[id] = true
		t.scopes = append(t.scopes, &costv1.Scope{Id: id, Parent: parent, Kind: kind, Name: name})
	}
	return id
}

func (t *Tree) Add(scope, vendor, typ, name, region string, properties map[string]any, unknown ...string) *costv1.Resource {
	resource := &costv1.Resource{
		Id:      scope + "/" + typ + ":" + name,
		Scope:   scope,
		Vendor:  vendor,
		Type:    typ,
		Name:    name,
		Region:  region,
		Unknown: unknown,
	}
	props, err := Struct(properties)
	if err != nil && t.err == nil {
		t.err = fmt.Errorf("%s: %w", resource.Id, err)
	}
	resource.Properties = props
	t.resources = append(t.resources, resource)
	return resource
}

func (t *Tree) AddShaped(scope, vendor, region string, shaped []Shaped) {
	for _, item := range shaped {
		t.Add(scope, vendor, item.Type, item.Name, region, item.Properties)
	}
}

func (t *Tree) Set(source string) (*costv1.ResourceSet, error) {
	if t.err != nil {
		return nil, t.err
	}
	return &costv1.ResourceSet{Source: source, Scopes: t.scopes, Resources: t.resources}, nil
}
