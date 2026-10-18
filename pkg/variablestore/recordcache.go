package variablestore

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/records"
)

type recordCache struct {
	store  Store
	scope  Scope
	byName map[string]records.Entry
}

func (s Store) newRecordCache(scope Scope) *recordCache {
	return &recordCache{store: s, scope: scope, byName: map[string]records.Entry{}}
}

func (p *recordCache) named(ctx context.Context, names []string) error {
	if len(names) == 1 {
		return p.load(ctx, bindingPrefix(p.scope, names[0]))
	}
	return p.all(ctx)
}

func (p *recordCache) all(ctx context.Context) error {
	return p.load(ctx, bindingsPrefix(p.scope))
}

func (p *recordCache) load(ctx context.Context, under records.Key) error {
	stored, err := p.store.listUnder(ctx, under)
	if err != nil {
		return fmt.Errorf("read %s's published bindings: %w", p.scope.Project, err)
	}
	for _, entry := range stored {
		p.byName[entry.Key.String()] = entry
	}
	return nil
}

func (p *recordCache) at(name records.Key) records.Entry { return p.byName[name.String()] }
