package deploy

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
)

type stateResource struct {
	kind   string
	name   string
	inputs resource.PropertyMap
}

func (r stateResource) key() string { return r.kind + "::" + r.name }

type engineState struct {
	mu      sync.Mutex
	stacks  map[string]map[string]stateResource
	locked  map[string]bool
	holding func(stack string)
}

func newEngineState() *engineState {
	return &engineState{stacks: map[string]map[string]stateResource{}, locked: map[string]bool{}}
}

func (s *engineState) lock(stack string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked[stack] {
		return fmt.Errorf("the stack is currently locked by 1 lock(s). Either wait for the other process(es) to end or delete the lock file with `pulumi cancel`: %s", stack)
	}
	s.locked[stack] = true
	return nil
}

func (s *engineState) unlock(stack string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.locked, stack)
}

func (s *engineState) hold(stack string) {
	if s.holding != nil {
		s.holding(stack)
	}
}

func (s *engineState) diff(stack string, declared map[string]stateResource) []provider.Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.stacks[stack]
	var changes []provider.Change
	for key, wanted := range declared {
		action := provider.ActionCreate
		if current, present := held[key]; present {
			action = provider.ActionKeep
			if !current.inputs.DeepEquals(wanted.inputs) {
				action = provider.ActionUpdate
			}
		}
		changes = append(changes, provider.Change{Kind: wanted.kind, Name: wanted.name, Action: action})
	}
	for key, current := range held {
		if _, kept := declared[key]; !kept {
			changes = append(changes, provider.Change{Kind: current.kind, Name: current.name, Action: provider.ActionDelete})
		}
	}
	slices.SortFunc(changes, func(a, b provider.Change) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	return changes
}

func (s *engineState) apply(stack string, declared map[string]stateResource, log progress.Log) {
	changes := s.diff(stack, declared)
	s.mu.Lock()
	s.stacks[stack] = maps.Clone(declared)
	s.mu.Unlock()
	if log == nil {
		return
	}
	now := time.Now()
	for _, change := range changes {
		if change.Action.Writes() {
			log.Span("resource "+string(change.Action), now, now, nil,
				provider.AttrResourceType(change.Kind), provider.AttrResourceName(change.Name), provider.AttrResourceAction(change.Action))
		}
	}
}

func (s *engineState) teardown(stack string) []provider.Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changes []provider.Change
	for _, current := range s.stacks[stack] {
		changes = append(changes, provider.Change{Kind: current.kind, Name: current.name, Action: provider.ActionDelete})
	}
	return changes
}

func (s *engineState) export(stack string) []pulumi.ExportedResource {
	s.mu.Lock()
	defer s.mu.Unlock()
	var exported []pulumi.ExportedResource
	for _, held := range s.stacks[stack] {
		exported = append(exported, pulumi.ExportedResource{Type: held.kind, ID: held.name + "-id"})
	}
	slices.SortFunc(exported, func(a, b pulumi.ExportedResource) int {
		return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.ID, b.ID))
	})
	return exported
}

func (s *engineState) forget(stack string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.stacks, stack)
}

type declaredMonitor struct {
	inner    sdk.MockResourceMonitor
	mu       sync.Mutex
	declared map[string]stateResource
}

func recordDeclared(inner sdk.MockResourceMonitor) *declaredMonitor {
	return &declaredMonitor{inner: inner, declared: map[string]stateResource{}}
}

func (d *declaredMonitor) NewResource(args sdk.MockResourceArgs) (string, resource.PropertyMap, error) {
	declared := stateResource{kind: args.TypeToken, name: args.Name, inputs: args.Inputs.Copy()}
	d.mu.Lock()
	d.declared[declared.key()] = declared
	d.mu.Unlock()
	return d.inner.NewResource(args)
}

func (d *declaredMonitor) Call(args sdk.MockCallArgs) (resource.PropertyMap, error) {
	return d.inner.Call(args)
}
