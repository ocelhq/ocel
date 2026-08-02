package envgate

import (
	"context"
	"fmt"
	"strings"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// keyDelimiter is what the store separates a value key's components with. It
// is forbidden in every user-chosen name so a key stays unambiguous to build
// and to parse back; the store client enforces the same rule from the other
// side of the provider socket, where this package cannot reach.
const keyDelimiter = "#"

// ValidateFolder rejects a folder path the store cannot address or a reader
// could mistake for another. Root is the absence of a folder, spelled as the
// empty string everywhere above the store, so "/" is rejected rather than
// silently accepted as a second spelling of it.
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

// Resolved is the cell one app reads a key from, once. Value is empty for a
// live-class key: those are addresses only, fetched from the store at runtime,
// which is what keeps their plaintext off a build host. Version is the store
// version of the cell it came from, carried for every class alike — what a
// ledger does with a version a runtime fetch cannot honour is the ledger's
// decision, not resolution's.
type Resolved struct {
	Folder  string
	Value   string
	Version int64
}

// Resolve decides where every declared key's value comes from for one app.
// This is the whole of folder resolution and it is exactly two hops — the
// app's bound folder, then the project root — so a value's origin is
// predictable without tracing a hierarchy. Nesting never participates: a
// folder is matched whole, never as a path prefix.
//
// A key scoped to folders the app does not bind is absent from the result
// rather than resolved from somewhere else, which is what makes an
// out-of-scope read a named failure at the point of use instead of a value the
// app was never meant to see.
func (g *Gate) Resolve(ctx context.Context, app string) (map[string]Resolved, error) {
	binding, known := g.binding(app)
	if !known {
		return nil, fmt.Errorf("app %q is not declared in this project's config, so it has no folder to resolve from", app)
	}

	g.mu.Lock()
	held := make(map[Cell]bool, len(g.cells))
	versions := make(map[Cell]int64, len(g.cells))
	for _, row := range g.cells {
		held[row.Cell] = true
		versions[row.Cell] = row.Version
	}
	definitions := append([]*resourcesv1.VariableDefinition(nil), g.definitions...)
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
		if h.live {
			resolved[h.cell.Key] = Resolved{Folder: h.cell.Folder, Version: versions[h.cell]}
			continue
		}
		if !plaintext[h.cell].found {
			continue
		}
		resolved[h.cell.Key] = Resolved{Folder: h.cell.Folder, Value: plaintext[h.cell].value, Version: versions[h.cell]}
	}
	return resolved, nil
}

// hop is the two-hop rule itself.
func hop(definition *resourcesv1.VariableDefinition, binding string, held map[Cell]bool) (Cell, bool) {
	if scope := definition.GetFolders(); len(scope) > 0 {
		if binding == "" || !contains(scope, binding) {
			return Cell{}, false
		}
		cell := Cell{Key: definition.GetKey(), Folder: binding}
		return cell, held[cell]
	}
	if binding != "" {
		if cell := (Cell{Key: definition.GetKey(), Folder: binding}); held[cell] {
			return cell, true
		}
	}
	cell := Cell{Key: definition.GetKey()}
	return cell, held[cell]
}

func (g *Gate) binding(app string) (string, bool) {
	for _, a := range g.scope.Apps {
		if a.Name == app {
			return a.Folder, true
		}
	}
	return "", false
}

// Lint reports what only reading the declarations and the app bindings
// together can see. A scope no app covers is a warning: the requirement is
// dead but nothing is wrong. A scope only some of whose folders are bound is
// an error, because a scoped variable exists to diverge across every folder it
// names — a folder left unread means one half of a rename landed and the other
// did not, so the message names both places it has to agree in.
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
					"Rename the folder in both, or drop %s from the scope.",
				definition.GetKey(), strings.Join(scope, " and "), strings.Join(unbound, " or "),
				source(definition), configPath, strings.Join(unbound, " or "))
		}
	}
	return warnings, nil
}

// source names where a declaration was written. The SDK fills it best-effort,
// so an empty one falls back to naming the key, which is what a reader greps
// for anyway.
func source(definition *resourcesv1.VariableDefinition) string {
	if s := definition.GetSource(); s != "" {
		return s
	}
	return "the defineEnv call declaring " + definition.GetKey()
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// CheckWritable refuses a write to a cell nothing could read back. A scoped
// key diverges across the folders it names and has no root value at all, so a
// root write, or one to a folder outside the scope, would put a value in the
// store that no app resolves — the store's own client cannot see this, because
// scoping is declared in code and never recorded beside the value.
//
// A key no declaration mentions is writable anywhere: values may be set before
// the code that reads them exists.
func CheckWritable(definitions []*resourcesv1.VariableDefinition, key, folder string) error {
	for _, definition := range definitions {
		if definition.GetKey() != key {
			continue
		}
		scope := definition.GetFolders()
		if len(scope) == 0 || contains(scope, folder) {
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
