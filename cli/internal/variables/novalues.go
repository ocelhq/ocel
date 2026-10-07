package variables

import "context"

type NoValues struct{}

func (NoValues) List(context.Context) ([]ValueMetadata, error) { return nil, nil }

func (NoValues) Reveal(context.Context, []Coordinate) (map[Coordinate]string, error) {
	return nil, nil
}
