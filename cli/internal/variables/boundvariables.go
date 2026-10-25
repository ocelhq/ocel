package variables

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/project"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func (s Scope) boundTo(key string) (project.BoundVariable, bool) {
	at := slices.IndexFunc(s.Bound, func(bound project.BoundVariable) bool { return bound.Key == key })
	if at < 0 {
		return project.BoundVariable{}, false
	}
	return s.Bound[at], true
}

func (s Scope) unbound(definitions []*resourcesv1.VariableDefinition) []*resourcesv1.VariableDefinition {
	out := make([]*resourcesv1.VariableDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if _, bound := s.boundTo(definition.GetKey()); !bound {
			out = append(out, definition)
		}
	}
	return out
}

func (d *Declarations) boundFor(app string) []project.BoundVariable {
	at := slices.IndexFunc(d.scope.Apps, func(a App) bool { return a.Name == app })
	if at < 0 {
		return nil
	}
	var out []project.BoundVariable
	for _, definition := range declaredForApp(d.scope.Apps[at], d.Definitions()) {
		if bound, ok := d.scope.boundTo(definition.GetKey()); ok && !slices.Contains(out, bound) {
			out = append(out, bound)
		}
	}
	return out
}

func (d *Declarations) RefuseBoundVariableConflicts() error {
	definitions := d.Definitions()
	d.mu.Lock()
	stored := map[string]bool{}
	for _, row := range d.cells {
		stored[row.Cell.Key] = true
	}
	for cell := range d.overrides {
		stored[cell.Key] = true
	}
	d.mu.Unlock()

	for _, bound := range d.scope.Bound {
		if !slices.ContainsFunc(definitions, func(definition *resourcesv1.VariableDefinition) bool { return definition.GetKey() == bound.Key }) {
			return fmt.Errorf("`bindings.variables` binds %s to %q, and no app declares %s. Declare it with `ocel/env`, or drop it from `bindings.variables`",
				bound.Key, bound.Spelled(), bound.Key)
		}
		if stored[bound.Key] {
			return fmt.Errorf("%s is bound to %q by `bindings.variables` and also has a value set here: a variable takes its value from one place. Remove the value with `ocel env rm %s`, or drop %s from `bindings.variables`",
				bound.Key, bound.Spelled(), bound.Key, bound.Key)
		}
		for _, app := range d.scope.Apps {
			if slices.Contains(inlinedKeys(ClientApp{App: app, Declared: declaredForApp(app, definitions)}), bound.Key) {
				return fmt.Errorf("app %s inlines %s into its client bundle at build, and %s is bound to %q, which a deploy reads after the build. Set %s with `ocel env set` instead",
					app.Name, bound.Key, bound.Key, bound.Spelled(), bound.Key)
			}
		}
	}
	return nil
}

func RefuseBound(scope Scope, key string) error {
	bound, ok := scope.boundTo(key)
	if !ok {
		return nil
	}
	return fmt.Errorf("%s is bound to %q by `bindings.variables`, so every deploy reads it off that record and a value set here would reach nothing. Publish the value there, or drop %s from `bindings.variables`",
		key, bound.Spelled(), key)
}
