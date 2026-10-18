package providerserver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

func (r *deployRun) publish(ctx context.Context, bindings []provider.Binding) error {
	publishing := make([]variablestore.NamedBindingWrite, 0, len(bindings))
	for _, binding := range bindings {
		message, err := provider.BindingMessage(binding)
		if err != nil {
			return err
		}
		if err := variablestoreserver.VerifyGrantScope(message); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
		pair, err := variablestoreserver.BindingPair(variablestore.OwnerOcel, message)
		if err != nil {
			return err
		}
		publishing = append(publishing, variablestore.NamedBindingWrite{Name: binding.Name, Write: pair})
	}
	if _, err := r.values.SetBindings(ctx, r.scope, bindingEnvironment(r.spec), variablestore.OwnerOcel, publishing); err != nil {
		return fmt.Errorf("publish %s's bindings: %w", r.scope.Project, err)
	}
	if err := r.pruneBindings(ctx, bindings); err != nil {
		return err
	}
	r.publishedBindings().forget()
	return nil
}

func (r *deployRun) pruneBindings(ctx context.Context, bindings []provider.Binding) error {
	environment := bindingEnvironment(r.spec)
	published, err := r.values.ListBindings(ctx, r.scope, environment)
	if err != nil {
		return fmt.Errorf("read %s's published bindings: %w", r.scope.Project, err)
	}
	stale := map[string]int64{}
	for _, record := range published {
		if record.Owner != variablestore.OwnerOcel || record.Environment != environment {
			continue
		}
		if slices.ContainsFunc(bindings, func(binding provider.Binding) bool { return binding.Name == record.Name }) {
			continue
		}
		stale[record.Name] = record.Version
	}
	if len(stale) == 0 {
		return nil
	}
	if _, err := r.values.RemoveUnchangedBindings(ctx, r.scope, environment, stale); err != nil {
		return fmt.Errorf("prune %s's published bindings: %w", r.scope.Project, err)
	}
	return nil
}

type publishedBindings struct {
	store       variablestore.Store
	scope       variablestore.Scope
	environment string

	mu       sync.Mutex
	carried  []provider.Binding
	resolved []provider.Binding
	loaded   bool
}

func (p *publishedBindings) carry(bindings []provider.Binding) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.carried = bindings
	p.resolved, p.loaded = nil, false
}

func (p *publishedBindings) forget() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolved, p.loaded = nil, false
}

func (p *publishedBindings) Published(ctx context.Context) ([]provider.Binding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return p.resolved, nil
	}
	names, err := p.store.PublishedNames(ctx, p.scope, p.environment)
	if err != nil {
		return nil, err
	}
	resolved, err := p.store.ResolveBindings(ctx, p.scope, p.environment, names)
	if err != nil {
		return nil, err
	}
	bindings := slices.Clone(p.carried)
	for i, published := range resolved {
		if slices.ContainsFunc(p.carried, func(carried provider.Binding) bool { return carried.Name == names[i] }) {
			continue
		}
		binding, err := bindingPublished(names[i], published)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	slices.SortFunc(bindings, func(a, b provider.Binding) int { return strings.Compare(a.Name, b.Name) })
	p.resolved, p.loaded = bindings, true
	return bindings, nil
}

func (p *publishedBindings) Names(ctx context.Context) ([]string, error) {
	bindings, err := p.Published(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		names = append(names, binding.Name)
	}
	return names, nil
}

func (p *publishedBindings) Named(ctx context.Context, name string) (provider.Binding, error) {
	bindings, err := p.Published(ctx)
	if err != nil {
		return provider.Binding{}, err
	}
	for _, binding := range bindings {
		if binding.Name == name {
			return binding, nil
		}
	}
	published, err := p.store.ResolveBinding(ctx, p.scope, p.environment, name)
	if err != nil {
		return provider.Binding{}, err
	}
	return bindingPublished(name, published)
}

func bindingPublished(name string, published variablestore.StoredBinding) (provider.Binding, error) {
	message, err := variablestoreserver.DecodeBinding(published.Value)
	if err != nil {
		return provider.Binding{}, fmt.Errorf("read binding %s: %w", name, err)
	}
	binding := provider.BindingOf(message)
	binding.Version = published.Version
	return binding, nil
}
