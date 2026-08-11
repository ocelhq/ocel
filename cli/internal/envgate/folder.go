package envgate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

const keyDelimiter = "#"

func ValidateFolder(folder string) error {
	switch {
	case folder == "":
		return fmt.Errorf("a folder is required")
	case !strings.HasPrefix(folder, "/"):
		return fmt.Errorf("folder %q must start with %q", folder, "/")
	case folder == "/":
		return fmt.Errorf("folder %q is the project root, which is what an unbound app already reads; leave the folder off instead", folder)
	case strings.HasSuffix(folder, "/"):
		return fmt.Errorf("folder %q must not end with %q", folder, "/")
	case strings.Contains(folder, "//"):
		return fmt.Errorf("folder %q has an empty path segment", folder)
	case strings.Contains(folder, keyDelimiter):
		return fmt.Errorf("folder %q may not contain %q", folder, keyDelimiter)
	}
	return nil
}

type Resolved struct {
	Folder  string
	Value   string
	Version int64
}

func (g *Gate) Resolve(ctx context.Context, app string) (map[string]Resolved, error) {
	binding, known := g.binding(app)
	if !known {
		return nil, fmt.Errorf("app %q is not declared in this project's config, so it has no folder to resolve from", app)
	}

	g.mu.Lock()
	held := g.resolvedCells()
	definitions := slices.Clone(g.definitions)
	g.mu.Unlock()

	type hopped struct {
		cell Cell
		live bool
	}
	hops := make([]hopped, 0, len(definitions))
	var wanted []Cell
	for _, definition := range definitions {
		cell, ok := hop(definition, binding, held)
		if !ok {
			continue
		}
		live := definition.GetClass() == resourcesv1.VariableClass_VARIABLE_CLASS_SECRET
		hops = append(hops, hopped{cell: cell, live: live})
		if !live {
			wanted = append(wanted, cell)
		}
	}
	plaintext, err := g.reveal(ctx, wanted)
	if err != nil {
		return nil, err
	}

	resolved := make(map[string]Resolved, len(hops))
	for _, h := range hops {
		from := Resolved{Folder: h.cell.Folder, Version: held[h.cell]}
		if !h.live {
			if !plaintext[h.cell].found {
				continue
			}
			from.Value = plaintext[h.cell].value
		}
		resolved[h.cell.Key] = from
	}
	return resolved, nil
}

func hop(definition *resourcesv1.VariableDefinition, binding string, held heldCells) (Cell, bool) {
	if scope := definition.GetFolders(); len(scope) > 0 {
		if binding == "" || !slices.Contains(scope, binding) {
			return Cell{}, false
		}
		cell := Cell{Key: definition.GetKey(), Folder: binding}
		return cell, held.has(cell)
	}
	if binding != "" {
		if cell := (Cell{Key: definition.GetKey(), Folder: binding}); held.has(cell) {
			return cell, true
		}
	}
	cell := Cell{Key: definition.GetKey()}
	return cell, held.has(cell)
}

func (g *Gate) binding(app string) (string, bool) {
	for _, a := range g.scope.Apps {
		if a.Name == app {
			return a.Folder, true
		}
	}
	return "", false
}

func Lint(definitions []*resourcesv1.VariableDefinition, apps []App, configPath string) ([]string, error) {
	bound := map[string]string{}
	for _, app := range apps {
		if app.Folder != "" {
			bound[app.Folder] = app.Name
		}
	}

	var warnings []string
	for _, definition := range definitions {
		scope := definition.GetFolders()
		if len(scope) == 0 {
			continue
		}

		var unbound []string
		for _, folder := range scope {
			if err := ValidateFolder(folder); err != nil {
				return nil, fmt.Errorf("%s is scoped to an unusable folder: %w", definition.GetKey(), err)
			}
			if _, ok := bound[folder]; !ok {
				unbound = append(unbound, folder)
			}
		}

		switch {
		case len(unbound) == 0:
		case len(unbound) == len(scope):
			warnings = append(warnings, fmt.Sprintf(
				"%s is scoped to %s, which no app binds. Nothing reads it — bind an app to one of those folders in %s, or drop the scope.",
				definition.GetKey(), strings.Join(scope, " and "), configPath))
		default:
			return nil, fmt.Errorf(
				"%s is scoped to %s, but no app binds %s.\n"+
					"A scoped variable must diverge across every folder it names, so this is a folder rename that landed in one place only.\n"+
					"  declared in %s\n"+
					"  bound in    %s\n"+
					"Rename the folder in both, or drop %s from the scope",
				definition.GetKey(), strings.Join(scope, " and "), strings.Join(unbound, " or "),
				source(definition), configPath, strings.Join(unbound, " or "))
		}
	}
	return warnings, nil
}

func source(definition *resourcesv1.VariableDefinition) string {
	if s := definition.GetSource(); s != "" {
		return s
	}
	return "the defineEnv call declaring " + definition.GetKey()
}

func CheckWritable(definitions []*resourcesv1.VariableDefinition, key, folder string) error {
	for _, definition := range definitions {
		if definition.GetKey() != key {
			continue
		}
		scope := definition.GetFolders()
		if len(scope) == 0 || slices.Contains(scope, folder) {
			return nil
		}
		if folder == "" {
			return fmt.Errorf("%s is scoped to %s, so it has no value at the project root — nothing would read one. Set it with --folder %s instead",
				key, strings.Join(scope, " and "), scope[0])
		}
		return fmt.Errorf("%s is scoped to %s, so %s holds no value for it. Set it in one of the folders it names, or widen the scope where it is declared",
			key, strings.Join(scope, " and "), folder)
	}
	return nil
}
