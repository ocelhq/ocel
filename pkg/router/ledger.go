package router

import "context"

type Ledger interface {
	SchemaVersion(ctx context.Context) (int, error)

	PutStaged(ctx context.Context, record DeploymentRecord) error

	History(ctx context.Context, pointer string) ([]HistoryEntry, error)

	Prune(ctx context.Context, keepN int, pointer string) (PruneResult, error)

	RemovePointer(ctx context.Context, pointer string) (PruneResult, error)
}
