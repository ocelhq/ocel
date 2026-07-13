package deploy

import (
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// Realization is how a resource is physically provisioned for an environment.
type Realization int

const (
	// RealizationReal provisions the resource as its own real infrastructure —
	// the only faithful option for production, persistent previews, and any
	// resource type with no shared-substrate trick.
	RealizationReal Realization = iota
	// RealizationLogicalSlice provisions the resource as a cheap logical slice
	// carved from shared preview infrastructure (e.g. postgres → a logical
	// database in the shared serverless cluster). Only ephemeral previews use
	// it, and only for resource types that support the trick.
	RealizationLogicalSlice
)

// realizationFor decides how a resource of type rt is realized under an
// environment lifecycle. It is pure. Only an ephemeral-preview postgres resource
// is sliced; everything else — every other type, and anything persistent or
// unspecified — is realized as real infrastructure.
func realizationFor(rt resourcesv1.ResourceType, lifecycle deploymentsv1.Environment_Lifecycle) Realization {
	if rt == resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES && lifecycle == deploymentsv1.Environment_LIFECYCLE_EPHEMERAL {
		return RealizationLogicalSlice
	}
	return RealizationReal
}
