package fake

import (
	"reflect"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func (r *Stacks) provisionedResources(ref provider.StackRef) []provider.Resource {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.resources[stackKey(ref)])
}

func planUpdates(plan provider.Plan, spec provider.StackSpec, provisioned []provider.Resource) provider.Plan {
	for at, group := range plan.Groups {
		updated := false
		for row, change := range group.Changes {
			if change.Action != provider.ActionKeep {
				continue
			}
			if declared, found := findDeclared(spec, change.Kind, change.Name); found && isUpdated(provisioned, declared) {
				group.Changes[row].Action = provider.ActionUpdate
				updated = true
			}
		}
		if updated {
			plan.Groups[at].Action, plan.Groups[at].Reason = provider.RollUp(group.Changes)
		}
	}
	return plan
}

func findDeclared(spec provider.StackSpec, kind, name string) (provider.Resource, bool) {
	at := slices.IndexFunc(spec.Resources, func(resource provider.Resource) bool {
		return resource.Binding == "" && string(resource.Type) == kind && resource.Name == name
	})
	if at < 0 {
		return provider.Resource{}, false
	}
	return spec.Resources[at], true
}

func isUpdated(provisioned []provider.Resource, declared provider.Resource) bool {
	at := slices.IndexFunc(provisioned, func(resource provider.Resource) bool {
		return resource.Type == declared.Type && resource.Name == declared.Name
	})
	return at >= 0 && !reflect.DeepEqual(provisioned[at], declared)
}

func reportChanges(log progress.Log, spec provider.StackSpec, recorded []provider.Binding, provisioned []provider.Resource, start time.Time) {
	for _, resource := range spec.Resources {
		if resource.Binding != "" {
			continue
		}
		action := provider.KeepOrCreate(slices.ContainsFunc(recorded, resources.BindingFor(resource)))
		if action == provider.ActionKeep && isUpdated(provisioned, resource) {
			action = provider.ActionUpdate
		}
		resources.ReportChange(log, string(resource.Type), resource.Name, action, start, nil)
	}
	for _, binding := range recorded {
		if _, declared := findDeclared(spec, string(binding.Type), binding.Name); !declared {
			resources.ReportChange(log, string(binding.Type), binding.Name, provider.ActionDelete, start, nil)
		}
	}
}
