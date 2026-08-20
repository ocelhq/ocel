package server

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type fakeDNSWriter struct {
	written []edge.Record
	deleted []edge.Record
	err     error
}

func (f *fakeDNSWriter) EnsureRecords(_ context.Context, records []edge.Record, _ func(string)) ([]edge.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.written = append(f.written, records...)
	return records, nil
}

func (f *fakeDNSWriter) DeleteRecords(_ context.Context, records []edge.Record) error {
	f.deleted = append(f.deleted, records...)
	return nil
}
