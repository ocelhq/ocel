package kv

import (
	"fmt"
	"time"
)

// An EntryOption tunes an entry [NewText], [NewCounter], [NewJSON], [NewList] or [NewSet]
// declares.
type EntryOption interface {
	applyKVEntry(*kvEntrySettings)
}

// A WriteOption tunes how one write treats its key's TTL.
type WriteOption interface {
	applyKVWrite(*kvTTL)
}

// An EntryWriteOption tunes an entry's declaration, or one write to it.
type EntryWriteOption interface {
	EntryOption
	WriteOption
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

// TTL is how long a key lives after a write. On an entry it applies to every write; on
// one write it replaces the entry's own. It is at least a millisecond, written with its
// unit, such as 30*time.Second: a bare 30 is 30 nanoseconds, and refused.
func TTL(after time.Duration) EntryWriteOption {
	return kvTTLOption{kvTTL{mode: kvTTLExpire, after: after}}
}

// KeepTTL leaves the key's TTL as it is, so a write neither extends nor clears it.
func KeepTTL() WriteOption {
	return kvTTLOption{kvTTL{mode: kvTTLKeep}}
}

// NoTTL clears the key's TTL, so the key lives until it is deleted or evicted.
func NoTTL() WriteOption {
	return kvTTLOption{kvTTL{mode: kvTTLClear}}
}

type kvMissOnInvalidOption struct{}

func (kvMissOnInvalidOption) applyKVEntry(s *kvEntrySettings) { s.missOnInvalid = true }

// MissOnInvalid makes a [NewJSON] entry read a stored value that does not decode into
// its type as a miss, [ErrMiss], instead of an [InvalidValueError].
func MissOnInvalid() EntryOption {
	return kvMissOnInvalidOption{}
}

func resolveKVTTL(declared time.Duration, opts []WriteOption) (kvTTL, error) {
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
