// Package ledger — PROTOTYPE: the origin-provided ledger; would live under platform/aws.
package ledger

import (
	"context"

	cur "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/prototype/edge"
)

// Dynamo is one project's partition of the DynamoDB ledger table.
type Dynamo struct {
	Table string
	Slug  string
}

var _ edge.Ledger = Dynamo{}

func (d Dynamo) SchemaVersion(ctx context.Context) (int, error)              { panic("prototype") }
func (d Dynamo) PutStaged(ctx context.Context, r cur.DeploymentRecord) error { panic("prototype") }
func (d Dynamo) History(ctx context.Context, pointer string) ([]cur.HistoryEntry, error) {
	panic("prototype")
}
func (d Dynamo) Prune(ctx context.Context, keepN int, pointer string) (cur.PruneResult, error) {
	panic("prototype")
}
func (d Dynamo) Promote(ctx context.Context, p cur.Promotion, pointer string) error {
	panic("prototype")
}
func (d Dynamo) RemovePointer(ctx context.Context, pointer string) (cur.PruneResult, error) {
	panic("prototype")
}
