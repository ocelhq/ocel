package attribution

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type Declaration struct {
	Type  string
	ID    string
	Stack string
}

type App struct {
	Name string
	Path string
}

type Usage struct {
	App   string
	Type  string
	ID    string
	Files []string
}

type identity struct {
	typ string
	id  string
}

type UnresolvedDeclarationError struct {
	Type  string
	ID    string
	Stack string
}

func (e *UnresolvedDeclarationError) Error() string {
	return fmt.Sprintf(
		"attribution: cannot tell which project file declares %s %q, so no app can be granted it: %s",
		e.Type, e.ID, describeStack(e.Stack),
	)
}

func describeStack(stack string) string {
	if strings.TrimSpace(stack) == "" {
		return "the declaration reported no source location"
	}
	return "no frame in the declaration's source location points inside the project"
}

type UnresolvedImportError struct {
	App    string
	File   string
	Line   int
	Detail string
}

func (e *UnresolvedImportError) Error() string {
	return fmt.Sprintf(
		"attribution: app %q imports a module ocel cannot resolve without running it, at %s:%d: %s — ocel refuses the deploy rather than guess which resources %q reaches",
		e.App, e.File, e.Line, e.Detail, e.App,
	)
}

func Compute(root string, apps []App, declarations []Declaration) ([]Usage, error) {
	survivorsByApp := make(map[string]map[string]map[string]bool, len(apps))
	attributable := false
	for _, app := range apps {
		if app.Path == "" {
			continue
		}
		survivors, err := shakenSurvivors(root, app)
		if err != nil {
			return nil, err
		}
		survivorsByApp[app.Name] = survivors
		attributable = attributable || len(survivors) > 0
	}
	if !attributable {
		return nil, nil
	}

	declaringFiles := make(map[string][]Declaration, len(declarations))
	for _, d := range declarations {
		file, ok := DeclaringFile(root, d.Stack)
		if !ok {
			return nil, &UnresolvedDeclarationError{Type: d.Type, ID: d.ID, Stack: d.Stack}
		}
		declaringFiles[file] = append(declaringFiles[file], d)
	}

	var usages []Usage
	for _, app := range apps {
		survivors := survivorsByApp[app.Name]

		byResource := map[identity]*Usage{}
		for _, entry := range slices.Sorted(maps.Keys(survivors)) {
			for file := range survivors[entry] {
				for _, d := range declaringFiles[file] {
					id := identity{d.Type, d.ID}
					u, ok := byResource[id]
					if !ok {
						u = &Usage{App: app.Name, Type: d.Type, ID: d.ID}
						byResource[id] = u
					}
					if !slices.Contains(u.Files, entry) {
						u.Files = append(u.Files, entry)
					}
				}
			}
		}

		for _, u := range byResource {
			slices.Sort(u.Files)
			usages = append(usages, *u)
		}
	}

	slices.SortFunc(usages, func(a, b Usage) int {
		if c := strings.Compare(a.App, b.App); c != 0 {
			return c
		}
		if c := strings.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return usages, nil
}

var frameRE = regexp.MustCompile(`\(?((?:file://)?/[^):\s]+\.(?:ts|tsx|js|jsx|mjs|cjs)):(\d+):(\d+)\)?`)

func DeclaringFile(root, stack string) (string, bool) {
	for _, m := range frameRE.FindAllStringSubmatch(stack, -1) {
		rel, err := filepath.Rel(root, strings.TrimPrefix(m[1], "file://"))
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		rel = filepath.ToSlash(rel)
		if isVendored(rel) {
			continue
		}
		return rel, true
	}
	return "", false
}

func isVendored(rel string) bool {
	return slices.Contains(strings.Split(rel, "/"), "node_modules")
}
