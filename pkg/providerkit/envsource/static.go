package envsource

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

type static struct {
	id   string
	held map[values.Cell]Resolved
}

func Static(id string, held map[values.Cell]Resolved) Source {
	return static{id: id, held: held}
}

func (s static) ID() string { return s.id }

func (static) Capabilities() Caps { return Caps{Read: true, List: true} }

func (s static) Resolve(_ context.Context, folders []string) (map[values.Cell]Resolved, error) {
	out := make(map[values.Cell]Resolved, len(s.held))
	for at, held := range s.held {
		if slices.Contains(folders, at.Folder) {
			out[at] = held
		}
	}
	return out, nil
}

func (static) Put(context.Context, values.Cell, []byte, string) error { return ErrReadOnly }

func (static) Link(values.Cell) string { return "" }
