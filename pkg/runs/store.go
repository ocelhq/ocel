package runs

import (
	"context"
	"encoding/json"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

type Store interface {
	CreateRun(ctx context.Context, run Run) (records.Revision, error)
	ReadRun(ctx context.Context, topic, execution string) (Run, error)
	WriteRun(ctx context.Context, run Run) (records.Revision, error)
	DeleteRun(ctx context.Context, run Run) error
	ListRuns(ctx context.Context, filter RunFilter) (RunPage, error)
	CountRuns(ctx context.Context, filter RunFilter) (int64, error)

	EnsureRecord(ctx context.Context, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error)
	ReplaceRecord(ctx context.Context, record provider.ExpiringRecord, held json.RawMessage) error
	DeleteRecord(ctx context.Context, record provider.ExpiringRecord) error
}
