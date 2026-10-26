package variablestore

import (
	"context"
	"fmt"
)

type PublishedError struct {
	At        Coordinate
	Publisher string
}

func (e *PublishedError) Error() string {
	return fmt.Sprintf("%s is published by %s", e.At, e.Publisher)
}

func (s Store) SetPublished(ctx context.Context, scope Scope, at Coordinate, plaintext string, from Provenance, expected int64) (Metadata, error) {
	return s.write(ctx, scope, at, plaintext, from, &expected)
}

func (s Store) DeletePublished(ctx context.Context, scope Scope, at Coordinate, from Provenance, expected int64) (bool, error) {
	return s.remove(ctx, scope, at, from, &expected)
}

func refuseOtherPublisher(at Coordinate, current storedValue, from Provenance) error {
	if current.live() == 0 {
		return nil
	}
	return refusePublisher(at, current.Publisher, from)
}

func refusePublisher(at Coordinate, publisher string, from Provenance) error {
	if publisher == "" || publisher == from.Publisher {
		return nil
	}
	return &PublishedError{At: at, Publisher: publisher}
}
