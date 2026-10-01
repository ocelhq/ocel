package provider

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
)

type TaskStore interface {
	EnsureRecord(ctx context.Context, record ExpiringRecord) (ExpiringRecord, bool, error)

	ReadRun(ctx context.Context, execution string) (Run, error)

	WriteRun(ctx context.Context, run Run) (keyvalue.Revision, error)

	ListRuns(ctx context.Context, filter RunFilter) (RunPage, error)
}

type RecordPurpose string

const (
	RecordIdempotency   RecordPurpose = "idempotency"
	RecordDebounce      RecordPurpose = "debounce"
	RecordStagedPayload RecordPurpose = "staged-payload"
)

const DefaultIdempotencyKeyLife = 30 * 24 * time.Hour

type ExpiringRecord struct {
	Purpose   RecordPurpose
	Topic     string
	Key       string
	Value     json.RawMessage
	ExpiresAt time.Time
}

type RunStatus string

const (
	RunDelayed   RunStatus = "delayed"
	RunQueued    RunStatus = "queued"
	RunExecuting RunStatus = "executing"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
	RunExpired   RunStatus = "expired"
	RunTimedOut  RunStatus = "timed-out"
)

type Run struct {
	Execution string
	Topic     string
	Consumer  string
	Status    RunStatus
	Payload   json.RawMessage
	Output    json.RawMessage
	Error     string
	Attempts  int
	Tags      []string
	Metadata  json.RawMessage

	CreatedAt  time.Time
	DueAt      time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	ExpiresAt  time.Time

	Revision keyvalue.Revision
}

type RunFilter struct {
	Topic    string
	Statuses []RunStatus
	Tags     []string
	Cursor   string
	Limit    int
}

type RunPage struct {
	Runs       []Run
	NextCursor string
}
