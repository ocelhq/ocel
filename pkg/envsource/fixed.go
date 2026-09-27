package envsource

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/envvars"
)

type fixed struct {
	id     string
	values map[envvars.Cell]Value
}

func NewFixed(id string, values map[envvars.Cell]Value) Source {
	return fixed{id: id, values: values}
}

func (s fixed) ID() string { return s.id }

func (s fixed) Read(_ context.Context, folders []string) (map[envvars.Cell]Value, error) {
	out := make(map[envvars.Cell]Value, len(s.values))
	for at, value := range s.values {
		if slices.Contains(folders, at.Folder) {
			out[at] = value
		}
	}
	return out, nil
}

func (fixed) Create(context.Context, envvars.Cell, []byte, string) error { return ErrReadOnly }

func (fixed) Update(context.Context, envvars.Cell, []byte, string) error { return ErrReadOnly }

func (fixed) URL(envvars.Cell) string { return "" }
