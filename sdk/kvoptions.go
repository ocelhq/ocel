package ocel

import (
	"fmt"
	"time"
)

// A KVEntryOption tunes an entry [KVText], [KVCounter], [KVJSON], [KVList] or [KVSet]
// declares.
type KVEntryOption interface {
	applyKVEntry(*kvEntrySettings)
}

// A KVWriteOption tunes how one write treats its key's TTL.
type KVWriteOption interface {
	applyKVWrite(*kvTTL)
}

// A KVEntryWriteOption tunes an entry's declaration, or one write to it.
type KVEntryWriteOption interface {
	KVEntryOption
	KVWriteOption
}

type kvTTLMode int

const (
	kvTTLDeclared kvTTLMode = iota
	kvTTLExpire
	kvTTLKeep
	kvTTLClear
)

type kvTTL struct {
	mode  kvTTLMode
	after time.Duration
}

type kvEntrySettings struct {
	ttl           time.Duration
	missOnInvalid bool
}

type kvTTLOption struct{ ttl kvTTL }

func (o kvTTLOption) applyKVEntry(s *kvEntrySettings) { s.ttl = o.ttl.after }
func (o kvTTLOption) applyKVWrite(t *kvTTL)           { *t = o.ttl }

// KVTTL is how long a key lives after a write. On an entry it applies to every write; on
// one write it replaces the entry's own. It is at least a millisecond, written with its
// unit, such as 30*time.Second: a bare 30 is 30 nanoseconds, and refused.
func KVTTL(after time.Duration) KVEntryWriteOption {
	return kvTTLOption{kvTTL{mode: kvTTLExpire, after: after}}
}

// KVKeepTTL leaves the key's TTL as it is, so a write neither extends nor clears it.
func KVKeepTTL() KVWriteOption {
	return kvTTLOption{kvTTL{mode: kvTTLKeep}}
}

// KVNoTTL clears the key's TTL, so the key lives until it is deleted or evicted.
func KVNoTTL() KVWriteOption {
	return kvTTLOption{kvTTL{mode: kvTTLClear}}
}

type kvMissOnInvalidOption struct{}

func (kvMissOnInvalidOption) applyKVEntry(s *kvEntrySettings) { s.missOnInvalid = true }

// KVMissOnInvalid makes a [KVJSON] entry read a stored value that does not decode into
// its type as a miss, [ErrKVMiss], instead of an [InvalidKVValueError].
func KVMissOnInvalid() KVEntryOption {
	return kvMissOnInvalidOption{}
}

func resolveKVTTL(declared time.Duration, opts []KVWriteOption) (kvTTL, error) {
	written := kvTTL{mode: kvTTLDeclared}
	for _, opt := range opts {
		opt.applyKVWrite(&written)
	}
	switch {
	case written.mode == kvTTLDeclared && declared == 0:
		return kvTTL{mode: kvTTLClear}, nil
	case written.mode == kvTTLDeclared:
		return kvTTL{mode: kvTTLExpire, after: declared}, nil
	case written.mode == kvTTLExpire:
		if err := refuseShortKVTTL(written.after); err != nil {
			return kvTTL{}, fmt.Errorf("ocel: kv %w", err)
		}
	}
	return written, nil
}

func refuseShortKVTTL(ttl time.Duration) error {
	switch {
	case ttl >= time.Millisecond:
		return nil
	case ttl > 0:
		return fmt.Errorf("a ttl is at least 1ms, and %s is shorter: a TTL is a time.Duration, so write it with its unit, such as %d*time.Second", ttl, int64(ttl))
	}
	return fmt.Errorf("a ttl is at least 1ms, and %s is shorter", ttl)
}
