package providerkit

import (
	"context"
	"errors"
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
// A vendor supplies durable bytes with revisions and nothing more. Every
// record's name, shape and migration is kit logic, so two vendors cannot drift
// into remembering different things. The one memory that is not reached here is
// the edge ledger: the kit reaches it only through the edge, and
// [providerkit/ledger] is what an edge composes when it hosts no ledger of its
// own.
//
// [providerkit/ledger]: https://pkg.go.dev/github.com/ocelhq/ocel/pkg/providerkit/ledger
type RecordStore interface {
	// Read returns the record at a name. A name that was never written is not an
	// error: it is a zero Record, and its zero Revision is what Write then treats
	// as "this must not exist yet".
	Read(ctx context.Context, name RecordName) (Record, error)

	// Write stores bytes at a name if the record's Revision still matches what is
	// stored. A mismatch is [ErrStale]. This is not a nicety: a promotion pointer
	// flip is a compare-and-set or it is a race, and rollback is that same flip.
	Write(ctx context.Context, record Record) (Revision, error)

	// Remove drops a name if expected still matches, and returns [ErrStale] if it
	// does not.
	Remove(ctx context.Context, name RecordName, expected Revision) error

	// List returns the records beneath a name — bytes and revisions, not names.
	// Every candidate store already answers in whole records (a Query, a
	// collection get, a SELECT, a readdir and read), and a names-only List would
	// make History N+1 for no store's benefit. Depth is the store's problem, not
	// the kit's: everything under the prefix, in any order.
	List(ctx context.Context, under RecordName) ([]Record, error)
}

// ErrStale is a Write or Remove whose expected Revision no longer matches what
// is stored. It is deliberately not a Refusal: the call site decides what it
// means, and the two answers are far apart — a CAS loop re-reads and retries,
// while a pointer flip tells the user another deploy moved it.
var ErrStale = errors.New("the record moved since it was read")

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
