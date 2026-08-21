package providerkit

import (
	"context"
	"errors"
	"strings"
)

type RecordStore interface {
	Read(ctx context.Context, name RecordName) (Record, error)

	Write(ctx context.Context, record Record) (Revision, error)

	Remove(ctx context.Context, name RecordName, expected Revision) error

	List(ctx context.Context, under RecordName) ([]Record, error)
}

var ErrStale = errors.New("the record moved since it was read")

var ErrNoRecord = errors.New("no such record")

type RecordName []string

func (n RecordName) String() string { return strings.Join(n, "/") }

type Revision string

type Record struct {
	Name     RecordName
	Bytes    []byte
	Revision Revision
}
