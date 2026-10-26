package surface

import (
	"context"

	"github.com/ocelhq/ocel/pkg/provider/transform"
)

type RecordedPatch[D, U any] struct {
	Patched[D, U]

	record Record
	before Recorded
}

func (s Surface[D, U]) PatchRecorded(ctx context.Context, record Record, resource string, desired D, result transform.Result) (RecordedPatch[D, U], error) {
	before, err := record.Read(ctx)
	if err != nil {
		return RecordedPatch[D, U]{}, err
	}
	patched, err := s.Patch(resource, desired, result.Patches[s.Name], result.Tags, before)
	if err != nil {
		return RecordedPatch[D, U]{}, err
	}
	return RecordedPatch[D, U]{Patched: patched, record: record, before: before}, nil
}

func (p RecordedPatch[D, U]) Apply(ctx context.Context, apply func() error) error {
	return p.record.Apply(ctx, p.before, p.Recorded(), apply)
}
