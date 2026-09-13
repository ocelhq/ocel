package costkit

import (
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

type Tree struct {
	scopes    []*costv1.Scope
	resources []*costv1.Resource
	seen      map[string]bool
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
		Id:         scope + "/" + typ + ":" + name,
		Scope:      scope,
		Vendor:     vendor,
		Type:       typ,
		Name:       name,
		Region:     region,
		Properties: Struct(properties),
		Unknown:    unknown,
	}
	t.resources = append(t.resources, resource)
	return resource
}

func (t *Tree) Set(source string) *costv1.ResourceSet {
	return &costv1.ResourceSet{Source: source, Scopes: t.scopes, Resources: t.resources}
}
