package variables

import (
	"context"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type NoValues struct{}

func (NoValues) List(context.Context) ([]ValueMetadata, error) { return nil, nil }

func (NoValues) Classify(context.Context, map[Cell]resourcesv1.VariableClass) error { return nil }

func (NoValues) Deliver(context.Context, []Coordinate) (map[Coordinate]string, error) {
	return nil, nil
}
