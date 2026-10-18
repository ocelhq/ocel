package fake

import (
	"context"
	"reflect"
	"sync"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

type provisionedPrimitives struct {
	mu   sync.Mutex
	held map[string]provider.Resource
}

func primitiveKey(ref provider.StackRef, kind provider.BindingType, name string) string {
	return stackKey(ref) + "/" + string(kind) + "/" + name
}

func (p *Provider) primitiveHooks() *resources.ResourceHooks {
	return &resources.ResourceHooks{Plan: p.planPrimitive, Provision: p.provisionPrimitive, Remove: p.removePrimitive}
}

func (p *Provider) planPrimitive(_ context.Context, in resources.ProvisionRequest, _ *provider.Binding) (provider.ChangeAction, string, error) {
	p.primitives.mu.Lock()
	defer p.primitives.mu.Unlock()
	held, present := p.primitives.held[primitiveKey(in.Ref, in.Resource.Type, in.Resource.Name)]
	switch {
	case !present:
		return provider.ActionCreate, "", nil
	case !reflect.DeepEqual(held, in.Resource):
		return provider.ActionUpdate, "its declared spec changed", nil
	default:
		return provider.ActionKeep, provider.ReasonCurrent, nil
	}
}

func (p *Provider) provisionPrimitive(_ context.Context, in resources.ProvisionRequest, _ progress.Log) (provider.Binding, error) {
	p.primitives.mu.Lock()
	defer p.primitives.mu.Unlock()
	if p.primitives.held == nil {
		p.primitives.held = map[string]provider.Resource{}
	}
	p.primitives.held[primitiveKey(in.Ref, in.Resource.Type, in.Resource.Name)] = in.Resource
	return p.stacks.bindingFor(in.Resource), nil
}

func (p *Provider) removePrimitive(_ context.Context, ref provider.StackRef, binding provider.Binding, _ progress.Log) error {
	p.primitives.mu.Lock()
	defer p.primitives.mu.Unlock()
	delete(p.primitives.held, primitiveKey(ref, binding.Type, binding.Name))
	return nil
}
