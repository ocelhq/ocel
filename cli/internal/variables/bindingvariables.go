package variables

import (
	"context"
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/english"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type BindingVariables struct {
	Group string
	Site  string
	Keys  []string
}

func ImpliedDeclarations(scope Scope) ([]*resourcesv1.VariableDefinition, []*resourcesv1.GroupDefinition) {
	return scope.impliedDefinitions(), scope.impliedGroups()
}

func (d *Declarations) Declared() []*resourcesv1.VariableDefinition {
	return append(d.Definitions(), d.scope.impliedDefinitions()...)
}

func (s Scope) impliedDefinitions() []*resourcesv1.VariableDefinition {
	out := s.bindingDefinitions()
	for _, definition := range s.EnvSource.credentialDefinitions(s.envSourceSite()) {
		if !slices.ContainsFunc(out, func(existing *resourcesv1.VariableDefinition) bool { return existing.GetKey() == definition.GetKey() }) {
			out = append(out, definition)
		}
	}
	return out
}

func (s Scope) envSourceSite() string {
	if s.isPreview() {
		return "envSource.preview"
	}
	return "envSource.production"
}

func (s Scope) impliedGroups() []*resourcesv1.GroupDefinition {
	return append(s.bindingGroups(), s.EnvSource.credentialGroups()...)
}

func (s Scope) bindingDefinitions() []*resourcesv1.VariableDefinition {
	var out []*resourcesv1.VariableDefinition
	for _, bound := range s.Bindings {
		for _, key := range bound.Keys {
			if slices.ContainsFunc(out, func(existing *resourcesv1.VariableDefinition) bool { return existing.GetKey() == key }) {
				continue
			}
			out = append(out, &resourcesv1.VariableDefinition{
				Key:         key,
				Class:       resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE,
				Required:    true,
				Group:       bound.Group,
				Source:      bound.Site,
				Description: "Read by " + sites(s.readers(key)) + " at deploy; never handed to an app.",
			})
		}
	}
	return out
}

func (s Scope) bindingGroups() []*resourcesv1.GroupDefinition {
	definitions := s.bindingDefinitions()
	out := make([]*resourcesv1.GroupDefinition, 0, len(s.Bindings))
	for _, bound := range s.Bindings {
		if !slices.ContainsFunc(definitions, func(existing *resourcesv1.VariableDefinition) bool { return existing.GetGroup() == bound.Group }) {
			continue
		}
		out = append(out, &resourcesv1.GroupDefinition{
			Key:         bound.Group,
			Required:    true,
			Description: "What " + bound.Site + " connects with.",
		})
	}
	return out
}

func (s Scope) readers(key string) []string {
	var out []string
	for _, bound := range s.Bindings {
		if slices.Contains(bound.Keys, key) {
			out = append(out, bound.Site)
		}
	}
	return out
}

func sites(readers []string) string {
	quoted := make([]string, len(readers))
	for i, site := range readers {
		quoted[i] = "`" + site + "`"
	}
	return english.And(quoted)
}

func collision(definitions []*resourcesv1.VariableDefinition, scope Scope) error {
	for _, definition := range definitions {
		declaredBy := definition.GetSource()
		if declaredBy == "" {
			declaredBy = "the app's code"
		}
		if scope.EnvSource.isCredential(definition.GetKey()) {
			return fmt.Errorf(
				"%s is declared by %s and is what ocel logs in to %s with: that credential reaches ocel alone, and declaring it too would hand the app the credential as a plain variable. "+
					"Rename one of them",
				definition.GetKey(), declaredBy, scope.EnvSource.ID)
		}
		readers := scope.readers(definition.GetKey())
		if len(readers) == 0 {
			readers = Scope{Bindings: scope.OtherTiers}.readers(definition.GetKey())
		}
		if len(readers) == 0 {
			continue
		}
		return fmt.Errorf(
			"%s is declared by %s and read by %s: a binding's variable reaches the binding alone, and declaring it too would hand the app the credential as a plain variable. "+
				"Rename one of them",
			definition.GetKey(), declaredBy, sites(readers))
	}
	return nil
}

func unsetBindingVariables(definitions []*resourcesv1.VariableDefinition, present presentCells) []*resourcesv1.VariableProblem {
	var problems []*resourcesv1.VariableProblem
	for _, definition := range definitions {
		if present.has(Cell{Key: definition.GetKey()}) {
			continue
		}
		problems = append(problems, &resourcesv1.VariableProblem{
			Key:  definition.GetKey(),
			Kind: resourcesv1.VariableProblem_KIND_MISSING,
		})
	}
	return problems
}

func (d *Declarations) ResolveBindingVariables(ctx context.Context) (map[string]string, error) {
	var cells []Cell
	for _, definition := range d.scope.bindingDefinitions() {
		cells = append(cells, Cell{Key: definition.GetKey()})
	}
	plaintext, err := d.reveal(ctx, cells)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(cells))
	for _, cell := range cells {
		revealedValue := plaintext[cell]
		if !revealedValue.found {
			return nil, fmt.Errorf("%s, which %s reads, has no value here", cell.Key, sites(d.scope.readers(cell.Key)))
		}
		out[cell.Key] = revealedValue.value
	}
	return out, nil
}
