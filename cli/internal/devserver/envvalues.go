package devserver

import (
	"context"
	"maps"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/variables"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type envValues struct {
	mu           sync.Mutex
	values       map[string]string
	scope        variables.Scope
	store        *flatValues
	declarations *variables.Declarations

	declaring sync.Mutex
}

func newEnvValues() *envValues {
	return &envValues{}
}

func (e *envValues) use(values map[string]string, scope variables.Scope) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.values = values
	e.scope = scope
	e.store = newFlatValues(values)
	e.declarations = variables.NewDeclarations(e.store, scope)
}

func (e *envValues) snapshot() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return maps.Clone(e.values)
}

func (e *envValues) current() (*flatValues, *variables.Declarations) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store, e.declarations
}

func (e *envValues) forgetDeclarations() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.declarations == nil {
		return
	}
	e.store = newFlatValues(e.values)
	e.declarations = variables.NewDeclarations(e.store, e.scope)
}

func (e *envValues) declare(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	e.declaring.Lock()
	defer e.declaring.Unlock()

	store, declarations := e.current()
	if declarations == nil {
		return &resourcesv1.DeclareEnvResponse{}, nil
	}
	store.Declare(req.GetDefinitions())
	if err := declarations.Prefetch(ctx); err != nil {
		return nil, err
	}
	return declarations.DeclareEnv(ctx, req)
}

func (e *envValues) secretKeys() []string {
	store, _ := e.current()
	if store == nil {
		return nil
	}
	return store.sortedSecretKeys()
}
