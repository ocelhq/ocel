package runs

import (
	"context"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Store interface {
	CreateRun(ctx context.Context, run Run) (keyvalue.Revision, error)
	ReadRun(ctx context.Context, topic, execution string) (Run, error)
	WriteRun(ctx context.Context, run Run) (keyvalue.Revision, error)
	DeleteRun(ctx context.Context, run Run) error
	ListRuns(ctx context.Context, filter RunFilter) (RunPage, error)
	CountRuns(ctx context.Context, filter RunFilter) (int64, error)

	EnsureRecord(ctx context.Context, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error)
	WriteRecord(ctx context.Context, record provider.ExpiringRecord) error
	DeleteRecord(ctx context.Context, record provider.ExpiringRecord) error
}
