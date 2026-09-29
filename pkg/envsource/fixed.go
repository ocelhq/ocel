package envsource

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/variablestore"
)

type fixed struct {
	id     string
	values map[variablestore.Cell]Value
}

func NewFixed(id string, values map[variablestore.Cell]Value) Source {
	return fixed{id: id, values: values}
}

func (s fixed) ID() string { return s.id }

func (s fixed) Read(_ context.Context, folders []string) (map[variablestore.Cell]Value, error) {
	out := make(map[variablestore.Cell]Value, len(s.values))
	for at, value := range s.values {
		if slices.Contains(folders, at.Folder) {
			out[at] = value
		}
	}
	return out, nil
}

func (fixed) Create(context.Context, variablestore.Cell, []byte, string) error { return ErrReadOnly }

func (fixed) Update(context.Context, variablestore.Cell, []byte, string) error { return ErrReadOnly }

func (fixed) URL(variablestore.Cell) string { return "" }
