package attribution

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

type Declaration struct {
	Type   linksv1.LinkType
	Name   string
	Source string
}

type App struct {
	Name      string
	Path      string
	Language  discovery.Language
	Roots     []discovery.Root
	Container bool
	Members   []string
}

type Reachability func(file string) bool

type Reach interface {
	Entries(ctx context.Context, root string, app App) (map[string]Reachability, error)
}

var reaches = map[discovery.Language]Reach{discovery.JS: jsReach{}, discovery.Go: goReach{}, discovery.Python: pythonReach{}}

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
	Type   linksv1.LinkType
	Name   string
	Source string
}

func (e *UnresolvedDeclarationError) Error() string {
	return fmt.Sprintf(
		"attribution: cannot tell which project file declares %s %q, so no app can be granted it: the declaration names %q, which is not a project file",
		e.Type, e.Name, e.Source,
	)
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

func attributableLanguages() string {
	languages := make([]string, 0, len(reaches))
	for language := range reaches {
		languages = append(languages, string(language))
	}
	slices.Sort(languages)
	return listed(languages)
}

func listed(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func Compute(ctx context.Context, root string, apps []App, declarations []Declaration) ([]Usage, error) {
	if len(declarations) == 0 {
		return nil, nil
	}
	entriesByApp := make(map[string]map[string]Reachability, len(apps))
	attributable := false
	for _, app := range apps {
		if app.Path == "" {
			continue
		}
		if app.Language == "" {
			return nil, fmt.Errorf("attribution: app %q names no language", app.Name)
		}
		reach, ok := reaches[app.Language]
		if !ok {
			return nil, fmt.Errorf("attribution: app %q is a %s app, and this build of ocel attributes only %s apps", app.Name, app.Language, attributableLanguages())
		}
		entries, err := reach.Entries(ctx, root, app)
		if err != nil {
			return nil, err
		}
		entriesByApp[app.Name] = entries
		attributable = attributable || len(entries) > 0
	}
	if !attributable {
		return nil, nil
	}

	declaringFiles := make(map[string][]Declaration, len(declarations))
	for _, d := range declarations {
		site, ok := DeclaringSite(root, d.Source)
		if !ok {
			return nil, &UnresolvedDeclarationError{Type: d.Type, Name: d.Name, Source: d.Source}
		}
		declaringFiles[site.File] = append(declaringFiles[site.File], d)
	}

	var usages []Usage
	for _, app := range apps {
		entries := entriesByApp[app.Name]

		byResource := map[identity]*Usage{}
		for _, entry := range slices.Sorted(maps.Keys(entries)) {
			for file, declared := range declaringFiles {
				if !entries[entry](file) {
					continue
				}
				for _, d := range declared {
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

type Site struct {
	File string
	Line int
}

func (s Site) String() string {
	return fmt.Sprintf("%s:%d", s.File, s.Line)
}

func DeclaringSite(root, source string) (Site, bool) {
	colon := strings.LastIndex(source, ":")
	if colon <= 0 {
		return Site{}, false
	}
	path := source[:colon]
	line, err := strconv.Atoi(source[colon+1:])
	if err != nil {
		return Site{}, false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	rel, inside := relativeToRoot(root, filepath.Clean(path))
	if !inside || isVendored(rel) {
		return Site{}, false
	}
	return Site{File: rel, Line: line}, true
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

func runJSON(cmd *exec.Cmd) (*json.Decoder, string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return json.NewDecoder(&stdout), strings.TrimSpace(stderr.String()), err
}
