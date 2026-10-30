package properties

import (
	"fmt"
	"slices"

	"google.golang.org/protobuf/types/known/structpb"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

type Resource struct {
	ID          string
	Scope       string
	Vendor      string
	Type        string
	Name        string
	Region      string
	RegionKnown bool
	Properties  Properties
	References  map[string]string
}

func (r Resource) Proto() (*costv1.Resource, error) {
	unknown := r.Properties.Unknown
	if !r.RegionKnown && !slices.Contains(unknown, RegionPath) {
		unknown = append(slices.Clone(unknown), RegionPath)
		slices.Sort(unknown)
	}
	values, err := structpb.NewStruct(r.Properties.Values)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.ID, err)
	}
	return &costv1.Resource{
		Id:         r.ID,
		Scope:      r.Scope,
		Vendor:     r.Vendor,
		Type:       r.Type,
		Name:       r.Name,
		Region:     r.Region,
		Properties: values,
		Unknown:    unknown,
		Tags:       r.Properties.Tags(),
		References: r.References,
	}, nil
}

func DropUnresolvedReferences(resources []*costv1.Resource) {
	ids := make(map[string]bool, len(resources))
	for _, resource := range resources {
		ids[resource.GetId()] = true
	}
	for _, resource := range resources {
		for path, target := range resource.GetReferences() {
			if !ids[target] || target == resource.GetId() {
				delete(resource.References, path)
			}
		}
		if len(resource.GetReferences()) == 0 {
			resource.References = nil
		}
	}
}
