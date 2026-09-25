package envgate

import (
	"context"
	"fmt"
	"slices"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type Implied struct {
	Group string
	Site  string
	Keys  []string
}

func Declarations(implied []Implied) ([]*resourcesv1.VariableDefinition, []*resourcesv1.GroupDefinition) {
	scope := Scope{Implied: implied}
	return scope.impliedDefinitions(), scope.impliedGroups()
}

func (g *Gate) Declared() []*resourcesv1.VariableDefinition {
	return append(g.Definitions(), g.scope.impliedDefinitions()...)
}

func CheckImpliedWritable(implied []Implied, key, folder string) error {
	at, read := Scope{Implied: implied}.impliedAt(key)
	if !read || folder == "" {
		return nil
	}
	return fmt.Errorf("%s is read by `%s`, which serves the whole project, so it reads the value at the project root and a value in %s would reach nothing: set it without --folder", key, at.Site, folder)
}

func (s Scope) impliedDefinitions() []*resourcesv1.VariableDefinition {
	var out []*resourcesv1.VariableDefinition
	for _, implied := range s.Implied {
		for _, key := range implied.Keys {
			if slices.ContainsFunc(out, func(held *resourcesv1.VariableDefinition) bool { return held.GetKey() == key }) {
				continue
			}
			out = append(out, &resourcesv1.VariableDefinition{
				Key:         key,
				Class:       resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE,
				Required:    true,
				Group:       implied.Group,
				Source:      implied.Site,
				Description: "Read by " + implied.Site + " at deploy; never handed to an app.",
			})
		}
	}
	return out
}

func (s Scope) impliedGroups() []*resourcesv1.GroupDefinition {
	out := make([]*resourcesv1.GroupDefinition, 0, len(s.Implied))
	for _, implied := range s.Implied {
		out = append(out, &resourcesv1.GroupDefinition{
			Key:         implied.Group,
			Required:    true,
			Description: "What " + implied.Site + " connects with.",
		})
	}
	return out
}

func (s Scope) impliedAt(key string) (Implied, bool) {
	for _, implied := range s.Implied {
		if slices.Contains(implied.Keys, key) {
			return implied, true
		}
	}
	return Implied{}, false
}

func collision(definitions []*resourcesv1.VariableDefinition, scope Scope) error {
	for _, definition := range definitions {
		implied, read := scope.impliedAt(definition.GetKey())
		if !read {
			continue
		}
		declaredBy := definition.GetSource()
		if declaredBy == "" {
			declaredBy = "the app's code"
		}
		return fmt.Errorf(
			"%s is declared by %s and read by `%s`: a binding's variable reaches the binding alone, and declaring it too would hand the app the credential as a plain variable. "+
				"Rename one of them",
			definition.GetKey(), declaredBy, implied.Site)
	}
	return nil
}

func unsetImplied(definitions []*resourcesv1.VariableDefinition, held heldCells) []*resourcesv1.VariableProblem {
	var problems []*resourcesv1.VariableProblem
	for _, definition := range definitions {
		if held.has(Cell{Key: definition.GetKey()}) {
			continue
		}
		problems = append(problems, &resourcesv1.VariableProblem{
			Key:  definition.GetKey(),
			Kind: resourcesv1.VariableProblem_KIND_MISSING,
		})
	}
	return problems
}

func (g *Gate) ResolveImplied(ctx context.Context) (map[string]string, error) {
	var cells []Cell
	for _, definition := range g.scope.impliedDefinitions() {
		cells = append(cells, Cell{Key: definition.GetKey()})
	}
	plaintext, err := g.reveal(ctx, cells)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(cells))
	for _, cell := range cells {
		held := plaintext[cell]
		if !held.found {
			implied, _ := g.scope.impliedAt(cell.Key)
			return nil, fmt.Errorf("%s, which `%s` reads, has no value here", cell.Key, implied.Site)
		}
		out[cell.Key] = held.value
	}
	return out, nil
}
