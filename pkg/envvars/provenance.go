package envvars

import "context"

type Provenance struct {
	EnvSource string `json:"envSource,omitempty"`
	Version   string `json:"envSourceVersion,omitempty"`
}

func (s Store) SetFromEnvSource(ctx context.Context, scope Scope, at Coordinate, plaintext string, from Provenance, expected int64) (Metadata, error) {
	return s.write(ctx, scope, at, plaintext, from, &expected)
}

type Dereferenced struct {
	Project string
	Metadata
	Plaintext string
}

func (s Store) GetDereferenced(ctx context.Context, scope Scope, at Coordinate, reveal bool) (Dereferenced, error) {
	_, cell, err := s.cellAt(ctx, scope, at)
	if err != nil {
		return Dereferenced{}, err
	}
	if cell.live() == 0 {
		return Dereferenced{}, ErrNotFound
	}
	source, from, sourceAt, err := s.dereference(ctx, scope, at, cell)
	if err != nil {
		return Dereferenced{}, err
	}
	out := Dereferenced{Project: from.Project, Metadata: metadataOf(sourceAt, source)}
	if !reveal {
		return out, nil
	}
	plaintext, err := s.Cipher.Open(ctx, coordinateOf(from, sourceAt), source.Sealed)
	if err != nil {
		return Dereferenced{}, err
	}
	out.Plaintext = string(plaintext)
	return out, nil
}
