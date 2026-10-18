package runs

import (
	"encoding/json"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
)

type Run struct {
	Execution   string
	Topic       string
	Consumer    string
	Status      provider.RunStatus
	Payload     json.RawMessage
	Output      json.RawMessage
	Error       string
	Attempts    int
	MaxAttempts int
	Tags        []string
	Metadata    json.RawMessage

	Message     string
	PublishedAt time.Time
	Key         string
	Lane        provider.Lane
	Delivery    string
	Held        string
	LeaseUntil  time.Time

	CreatedAt  time.Time
	DueAt      time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	ExpiresAt  time.Time

	Revision records.Revision
}

type RunFilter struct {
	Topics   []string
	Consumer string
	Statuses []provider.RunStatus
	Tags     []string
	Cursor   string
	Limit    int
}

type RunPage struct {
	Runs       []Run
	NextCursor string
}

func (r Run) truncated() Run {
	for _, at := range []*time.Time{&r.PublishedAt, &r.LeaseUntil, &r.CreatedAt, &r.DueAt, &r.StartedAt, &r.FinishedAt, &r.ExpiresAt} {
		*at = at.Truncate(time.Microsecond)
	}
	return r
}

func (r Run) owns(claimed Run) bool {
	return r.Status == provider.RunExecuting && r.Delivery == claimed.Delivery && r.Attempts == claimed.Attempts
}
