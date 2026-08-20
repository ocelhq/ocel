package deploy

import (
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

type Realization int

const (
	RealizationReal Realization = iota
	RealizationLogicalSlice
)

func realizationFor(t linksv1.LinkType, lifecycle environmentv1.Lifecycle) Realization {
	if t == linksv1.LinkType_LINK_TYPE_POSTGRES && lifecycle == environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		return RealizationLogicalSlice
	}
	return RealizationReal
}
