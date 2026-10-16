package kv

import (
	"fmt"
	"time"
)

// An EntryOption tunes an entry [NewText], [NewCounter], [NewJSON], [NewList] or [NewSet]
// declares.
type EntryOption interface {
	applyEntry(*entrySettings)
}

// A WriteOption tunes how one write treats its key's TTL.
type WriteOption interface {
	applyWrite(*writeTTL)
}

// An EntryWriteOption tunes an entry's declaration, or one write to it.
type EntryWriteOption interface {
	EntryOption
	WriteOption
}

type ttlMode int

const (
	ttlDeclared ttlMode = iota
	ttlExpire
	ttlKeep
	ttlClear
)

type writeTTL struct {
	mode  ttlMode
	after time.Duration
}

type entrySettings struct {
	ttl           time.Duration
	missOnInvalid bool
}

type ttlOption struct{ ttl writeTTL }

func (o ttlOption) applyEntry(s *entrySettings) { s.ttl = o.ttl.after }
func (o ttlOption) applyWrite(t *writeTTL)      { *t = o.ttl }

// TTL is how long a key lives after a write. On an entry it applies to every write; on
// one write it replaces the entry's own. It is at least a millisecond, written with its
// unit, such as 30*time.Second: a bare 30 is 30 nanoseconds, and refused.
func TTL(after time.Duration) EntryWriteOption {
	return ttlOption{writeTTL{mode: ttlExpire, after: after}}
}

// KeepTTL leaves the key's TTL as it is, so a write neither extends nor clears it.
func KeepTTL() WriteOption {
	return ttlOption{writeTTL{mode: ttlKeep}}
}

// NoTTL clears the key's TTL, so the key lives until it is deleted or evicted.
func NoTTL() WriteOption {
	return ttlOption{writeTTL{mode: ttlClear}}
}

type missOnInvalidOption struct{}

func (missOnInvalidOption) applyEntry(s *entrySettings) { s.missOnInvalid = true }

// MissOnInvalid makes a [NewJSON] entry read a stored value that does not decode into
// its type as a miss, [ErrMiss], instead of an [InvalidValueError].
func MissOnInvalid() EntryOption {
	return missOnInvalidOption{}
}

func resolveTTL(declared time.Duration, opts []WriteOption) (writeTTL, error) {
	written := writeTTL{mode: ttlDeclared}
	for _, opt := range opts {
		opt.applyWrite(&written)
	}
	switch {
	case written.mode == ttlDeclared && declared == 0:
		return writeTTL{mode: ttlClear}, nil
	case written.mode == ttlDeclared:
		return writeTTL{mode: ttlExpire, after: declared}, nil
	case written.mode == ttlExpire:
		if err := refuseShortTTL(written.after); err != nil {
			return writeTTL{}, fmt.Errorf("ocel: kv %w", err)
		}
	}
	return written, nil
}

func refuseShortTTL(ttl time.Duration) error {
	switch {
	case ttl >= time.Millisecond:
		return nil
	case ttl > 0:
		return fmt.Errorf("a ttl is at least 1ms, and %s is shorter: a TTL is a time.Duration, so write it with its unit, such as %d*time.Second", ttl, int64(ttl))
	}
	return fmt.Errorf("a ttl is at least 1ms, and %s is shorter", ttl)
}
