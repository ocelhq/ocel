package devserver

import (
	"context"
	"slices"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	watchv1 "github.com/ocelhq/ocel/pkg/proto/devloop/watch/v1"
)

type envState struct {
	mu     sync.Mutex
	values map[string]string
	scope  envgate.Scope
	store  *flatValues
	gate   *envgate.Gate

	declaring sync.Mutex
}

func newEnvState() *envState {
	return &envState{}
}

func (e *envState) use(values map[string]string, scope envgate.Scope) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.values = values
	e.scope = scope
	e.store = newFlatValues(values)
	e.gate = envgate.New(e.store, scope)
}

func (e *envState) current() (*flatValues, *envgate.Gate) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store, e.gate
}

func (e *envState) forgetDeclarations() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.gate == nil {
		return
	}
	e.store = newFlatValues(e.values)
	e.gate = envgate.New(e.store, e.scope)
}

func (e *envState) declare(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	e.declaring.Lock()
	defer e.declaring.Unlock()

	store, gate := e.current()
	if gate == nil {
		return &resourcesv1.DeclareEnvResponse{}, nil
	}
	store.Declare(req.GetDefinitions())
	if err := gate.Prefetch(ctx); err != nil {
		return nil, err
	}
	return gate.DeclareEnv(ctx, req)
}

type liveKeys struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func newLiveKeys() *liveKeys {
	return &liveKeys{keys: make(map[string]struct{})}
}

func (l *liveKeys) declare(definitions []*resourcesv1.VariableDefinition) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, d := range definitions {
		if d.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET {
			l.keys[d.GetKey()] = struct{}{}
		}
	}
}

func (l *liveKeys) sorted() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	keys := make([]string, 0, len(l.keys))
	for key := range l.keys {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (l *liveKeys) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = make(map[string]struct{})
}

type configCache struct {
	mu  sync.Mutex
	cfg *resolve.Account
}

func newConfigCache() *configCache {
	return &configCache{}
}

func (c *configCache) use(cfg resolve.Account) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = &cfg
}

func (c *configCache) held() (resolve.Account, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg == nil {
		return resolve.Account{}, false
	}
	return *c.cfg, true
}

type envFanout struct {
	mu          sync.Mutex
	latest      *watchv1.EnvUpdate
	subscribers map[chan *watchv1.EnvUpdate]struct{}
}

func newEnvFanout() *envFanout {
	return &envFanout{subscribers: make(map[chan *watchv1.EnvUpdate]struct{})}
}

func (f *envFanout) push(update *watchv1.EnvUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latest = update
	for ch := range f.subscribers {
		select {
		case ch <- update:
		default:
		}
	}
}

func (f *envFanout) subscribe() chan *watchv1.EnvUpdate {
	ch := make(chan *watchv1.EnvUpdate, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.latest != nil {
		ch <- f.latest
	}
	f.subscribers[ch] = struct{}{}
	return ch
}

func (f *envFanout) unsubscribe(ch chan *watchv1.EnvUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subscribers, ch)
}
