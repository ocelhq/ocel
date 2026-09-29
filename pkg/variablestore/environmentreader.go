package variablestore

import (
	"context"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
)

type EnvironmentReader struct {
	KeyValues   keyvalue.Store
	Cipher      seal.Cipher
	Scope       Scope
	Environment string
}

func (r EnvironmentReader) Values(ctx context.Context, cells []Cell) (map[string]string, error) {
	store := Store{KeyValues: r.KeyValues, Cipher: r.Cipher}
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

	plaintexts := make(map[Coordinate]string, len(found))
	for _, value := range found {
		plaintexts[value.Coordinate] = value.Plaintext
	}
	out := make(map[string]string, len(cells))
	for _, at := range cells {
		for _, environment := range shadowing(r.Environment) {
			plaintext, ok := plaintexts[Coordinate{Cell: at, Environment: environment}]
			if !ok {
				continue
			}
			out[at.Key] = plaintext
			break
		}
	}
	return out, nil
}

func (r EnvironmentReader) Bindings(ctx context.Context, names []string) ([]StoredBinding, error) {
	store := Store{KeyValues: r.KeyValues, Cipher: r.Cipher}
	return store.ResolveBindings(ctx, r.Scope, r.Environment, names)
}
