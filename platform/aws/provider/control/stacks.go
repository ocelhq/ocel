package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type stacksWithoutEngine struct {
	provider.AbsentEngine
	keyValues records.Store
}

func (s stacksWithoutEngine) Resources(ctx context.Context, req provider.StackResources) ([]provider.PhysicalResource, error) {
	return stackrecords.ReadResources(ctx, s.keyValues, req)
}
