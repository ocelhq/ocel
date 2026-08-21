package providerkit

import "context"

// RecordStore is the durable memory of everything Ocel has done in this account:
// which projects exist, which stacks each one owns, which release each hostname
// is settled on, and which promotion the edge is currently pointing at.
//
// The hypothesis this stub takes, for [#508] to confirm or overturn: a vendor
// supplies a table and nothing more. Every record's shape, every key, every
// migration and every read is kit logic, so two vendors cannot drift into
// remembering different things. What a vendor cannot supply generically is
// atomicity, so Swap is the one verb with a precondition — a promotion pointer
// flip is a compare-and-set or it is a race.
//
// [#508]: https://github.com/ocelhq/ocel/issues/508
type RecordStore interface {
	Get(ctx context.Context, key RecordKey) ([]byte, error)

	Put(ctx context.Context, key RecordKey, value []byte) error

	// Swap writes next only if the stored value is still prior. A mismatch is a
	// Refusal with CodeOccupied, never a failure: someone else got there first
	// and the kit decides whether to retry or to tell the user.
	Swap(ctx context.Context, key RecordKey, prior, next []byte) error

	Delete(ctx context.Context, key RecordKey) error

	List(ctx context.Context, partition string) ([]RecordKey, error)
}

// RecordKey is a partition and a sort key, which is the smallest shape that maps
// onto a document store, a relational table and a directory of files alike. The
// kit builds every one of them.
type RecordKey struct {
	Partition string
	Sort      string
}
