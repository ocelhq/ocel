package attribution

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

type Declaration struct {
	Type  linksv1.LinkType
	Name  string
	Stack string
}

type App struct {
	Name string
	Path string
}

type Usage struct {
	App   string
	Type  linksv1.LinkType
	Name  string
	Files []string
}

type identity struct {
	typ  linksv1.LinkType
	name string
}

type UnresolvedDeclarationError struct {
	Type  linksv1.LinkType
	Name  string
	Stack string
}

func (e *UnresolvedDeclarationError) Error() string {
	return fmt.Sprintf(
		"attribution: cannot tell which project file declares %s %q, so no app can be granted it: %s",
		e.Type, e.Name, describeStack(e.Stack),
	)
}

func describeStack(stack string) string {
	if strings.TrimSpace(stack) == "" {
		return "the declaration reported no source location"
	}
	return "no frame of the reported source location names a project file outside node_modules"
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
		frame, ok := DeclaringFrame(root, d.Stack)
		if !ok {
			return nil, &UnresolvedDeclarationError{Type: d.Type, Name: d.Name, Stack: d.Stack}
		}
		declaringFiles[frame.File] = append(declaringFiles[frame.File], d)
	}

	var usages []Usage
	for _, app := range apps {
		survivors := survivorsByApp[app.Name]

		byResource := map[identity]*Usage{}
		for _, entry := range slices.Sorted(maps.Keys(survivors)) {
			for file := range survivors[entry] {
				for _, d := range declaringFiles[file] {
					key := identity{d.Type, d.Name}
					u, ok := byResource[key]
					if !ok {
						u = &Usage{App: app.Name, Type: d.Type, Name: d.Name}
						byResource[key] = u
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
		if c := cmp.Compare(a.Type, b.Type); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return usages, nil
}

var frameRE = regexp.MustCompile(`\(?((?:file://)?/[^):\s]+\.(?:ts|tsx|js|jsx|mjs|cjs)):(\d+):(\d+)\)?`)

type Frame struct {
	File string
	Line int
}

func (f Frame) String() string {
	return fmt.Sprintf("%s:%d", f.File, f.Line)
}

func DeclaringFrame(root, stack string) (Frame, bool) {
	for _, m := range frameRE.FindAllStringSubmatch(stack, -1) {
		rel, ok := relativeToRoot(root, strings.TrimPrefix(m[1], "file://"))
		if !ok || isVendored(rel) {
			continue
		}
		line, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		return Frame{File: rel, Line: line}, true
	}
	return Frame{}, false
}

func relativeToRoot(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func isVendored(rel string) bool {
	return slices.Contains(strings.Split(rel, "/"), "node_modules")
}
