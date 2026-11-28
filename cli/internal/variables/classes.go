package variables

import (
	"context"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func (d *Declarations) Classify(ctx context.Context) error {
	return d.values.Classify(ctx, d.classes())
}

func (d *Declarations) classes() map[Cell]resourcesv1.VariableClass {
	definitions := d.Declared()
	d.mu.Lock()
	present := d.resolvedCells()
	d.mu.Unlock()

	bindings := []string{""}
	for _, app := range d.scope.Apps {
		bindings = append(bindings, app.Folder)
	}
	out := map[Cell]resourcesv1.VariableClass{}
	for _, definition := range definitions {
		class := definition.GetClass()
		if !isStoredClass(class) {
			continue
		}
		for _, binding := range bindings {
			if cell, ok := resolveCell(definition, binding, present); ok {
				out[cell] = max(out[cell], class)
			}
		}
	}
	return out
}

func isStoredClass(class resourcesv1.VariableClass) bool {
	switch class {
	case resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, resourcesv1.VariableClass_VARIABLE_CLASS_SECRET:
		return true
	}
	return false
}
