package deploy

import (
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

type Realization int

const (
	RealizationReal Realization = iota
	RealizationLogicalSlice
)

func realizationFor(t linksv1.LinkType, lifecycle deploymentsv1.Environment_Lifecycle) Realization {
	if t == linksv1.LinkType_LINK_TYPE_POSTGRES && lifecycle == deploymentsv1.Environment_LIFECYCLE_EPHEMERAL {
		return RealizationLogicalSlice
	}
	return RealizationReal
}
