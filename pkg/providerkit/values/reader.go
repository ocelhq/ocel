package values

import (
	"context"
	"errors"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

type Reader struct {
	Records     ports.RecordStore
	Sealer      ports.Sealer
	Scope       Scope
	Environment string
}

func (r Reader) Values(ctx context.Context, cells []Cell) (map[string]string, error) {
	store := Store{Records: r.Records, Sealer: r.Sealer}
	out := make(map[string]string, len(cells))
	for _, at := range cells {
		for _, environment := range shadowing(r.Environment) {
			value, err := store.Get(ctx, r.Scope, Coordinate{Cell: at, Environment: environment}, true)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out[at.Key] = value.Plaintext
			break
		}
	}
	return out, nil
}

func (r Reader) Links(ctx context.Context, names []string) ([]Published, error) {
	store := Store{Records: r.Records, Sealer: r.Sealer}
	out := make([]Published, 0, len(names))
	for _, name := range names {
		published, err := store.ResolveLink(ctx, r.Scope, r.Environment, name)
		if err != nil {
			return nil, err
		}
		out = append(out, published)
	}
	return out, nil
}
