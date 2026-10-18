package provider

import (
	"encoding/json"
	"time"
)

type RecordPurpose string

const (
	RecordIdempotency RecordPurpose = "idempotency"
	RecordDebounce    RecordPurpose = "debounce"
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
