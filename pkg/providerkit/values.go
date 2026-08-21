package providerkit

import "context"

// ValueStore keeps a secret secret. It is separate from RecordStore for exactly
// one reason: these bytes are encrypted at rest under a key the bootstrap made,
// and only the vendor can encrypt them. Everything else about variables —
// validating a name, resolving a coordinate, mapping a tier to a class, gating
// which links a project may read, turning a miss into a wire code — is kit logic
// over this port.
//
// [#510] sharpens the method set; the boundary is what this stub claims.
//
// [#510]: https://github.com/ocelhq/ocel/issues/510
type ValueStore interface {
	Put(ctx context.Context, at Coordinate, value []byte) (Version, error)

	Get(ctx context.Context, at Coordinate) ([]byte, Version, error)

	Delete(ctx context.Context, at Coordinate) error

	List(ctx context.Context, within Coordinate) ([]Coordinate, error)

	Versions(ctx context.Context, at Coordinate) ([]Version, error)

	// Purge removes every value a project owns, in one call, because a project
	// leaving must not depend on the kit enumerating what it had.
	Purge(ctx context.Context, project string, report Reporter) (int, error)
}

// Coordinate addresses one value. The kit composes it from pkg/naming; the port
// stores it and hands it back.
type Coordinate struct {
	Project string
	Class   Class
	Env     string
	Link    string
	Name    string
}

// Version is one write of one value. Value bytes are never carried here — a
// listing must not decrypt.
type Version struct {
	ID      string
	Written string
	By      string
}
