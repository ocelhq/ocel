package providerkit

import (
	"context"
	"strings"
)

// RecordStore is the durable memory of everything Ocel has done in this account:
// which projects exist, which stacks each one owns, which release each hostname
// is settled on, which promotion the edge points at.
//
// It describes what the kit needs remembered, not how anything stores it. A name
// is a path, a revision is a token the store minted and the kit hands back
// unread, and that is the whole vocabulary — a DynamoDB table, a Durable Object,
// a SQLite file and a directory each satisfy it without the kit learning which
// one it is talking to.
//
// The hypothesis for [#508] to confirm or overturn: a vendor supplies durable
// bytes and nothing more, and every record's shape, key and migration is kit
// logic, so two vendors cannot drift into remembering different things.
//
// [#508]: https://github.com/ocelhq/ocel/issues/508
type RecordStore interface {
	// Read returns the record at a name. A name that was never written is not an
	// error: it is a zero Record, and its zero Revision is what Write then treats
	// as "this must not exist yet".
	Read(ctx context.Context, name RecordName) (Record, error)

	// Write stores bytes at a name if the record's Revision still matches what is
	// stored. A mismatch is a Refusal with CodeOccupied — someone else got there
	// first, and the kit decides whether to re-read and retry or to tell the user.
	// This is not a nicety: a promotion pointer flip is a compare-and-set or it is
	// a race, and rollback is that same flip.
	Write(ctx context.Context, record Record) (Revision, error)

	Remove(ctx context.Context, name RecordName, expected Revision) error

	// List returns the names beneath a name. Depth is the store's problem, not the
	// kit's: everything under the prefix, in any order.
	List(ctx context.Context, under RecordName) ([]RecordName, error)
}

// RecordName is a path, built entirely by the kit. A store maps it to whatever it
// has — two key columns, one string, a row id, a filename — and the kit never
// learns which.
type RecordName []string

func (n RecordName) String() string { return strings.Join(n, "/") }

// Revision is the store's own word for "this version". It is opaque: the kit
// reads one, carries it, and hands it back as a precondition. An ETag, a version
// attribute, a row id and a modification time are all valid, and the kit cannot
// tell them apart.
type Revision string

// Record is bytes at a name, with the revision they were read at.
type Record struct {
	Name     RecordName
	Bytes    []byte
	Revision Revision
}
