package provider

import (
	"context"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type VarScope struct {
	Slug        string
	Class       edge.Class
	Environment string
	App         string
}

type VarStore interface {
	Set(ctx context.Context, scope VarScope, key, value string, secret bool) (int64, error)
	Get(ctx context.Context, scope VarScope, key string) (Variable, bool, error)
	List(ctx context.Context, scope VarScope) ([]Variable, error)
	Delete(ctx context.Context, scope VarScope, key string) error
	SetLink(ctx context.Context, scope VarScope, resource string, link Link) error
	RemoveLink(ctx context.Context, scope VarScope, resource string) error
	Links(ctx context.Context, scope VarScope) ([]Link, error)
}
