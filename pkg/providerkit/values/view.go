package values

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

type View struct {
	Records     ports.RecordStore
	Cipher      ports.Cipher
	Scope       Scope
	Environment string
}

func (r View) Values(ctx context.Context, cells []Cell) (map[string]string, error) {
	store := Store{Records: r.Records, Cipher: r.Cipher}
	wanted := make([]Coordinate, 0, len(cells)*2)
	for _, at := range cells {
		for _, environment := range shadowing(r.Environment) {
			wanted = append(wanted, Coordinate{Cell: at, Environment: environment})
		}
	}
	found, err := store.Reveal(ctx, r.Scope, wanted)
	if err != nil {
		return nil, err
	}

	held := make(map[Coordinate]string, len(found))
	for _, value := range found {
		held[value.Coordinate] = value.Plaintext
	}
	out := make(map[string]string, len(cells))
	for _, at := range cells {
		for _, environment := range shadowing(r.Environment) {
			plaintext, ok := held[Coordinate{Cell: at, Environment: environment}]
			if !ok {
				continue
			}
			out[at.Key] = plaintext
			break
		}
	}
	return out, nil
}

func (r View) Bindings(ctx context.Context, names []string) ([]Published, error) {
	store := Store{Records: r.Records, Cipher: r.Cipher}
	return store.ResolveBindings(ctx, r.Scope, r.Environment, names)
}
