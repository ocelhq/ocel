package deploy

import (
	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type Realization int

const (
	RealizationReal Realization = iota
	RealizationLogicalSlice
)

func realizationFor(token string, lifecycle deploymentsv1.Environment_Lifecycle) Realization {
	if token == naming.TokenPostgres && lifecycle == deploymentsv1.Environment_LIFECYCLE_EPHEMERAL {
		return RealizationLogicalSlice
	}
	return RealizationReal
}
