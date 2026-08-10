package deploy

import (
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

type Realization int

const (
	RealizationReal Realization = iota
	RealizationLogicalSlice
)

func realizationFor(rt resourcesv1.ResourceType, lifecycle deploymentsv1.Environment_Lifecycle) Realization {
	if rt == resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES && lifecycle == deploymentsv1.Environment_LIFECYCLE_EPHEMERAL {
		return RealizationLogicalSlice
	}
	return RealizationReal
}
