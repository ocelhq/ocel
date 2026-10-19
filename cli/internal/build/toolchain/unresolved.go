package toolchain

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
)

type unresolved struct {
	mu      sync.Mutex
	imports []string
}

func (u *unresolved) plugin() api.Plugin {
	return api.Plugin{
		Name: "ocel-unresolved-dependency-import",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `^[^./]`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if _, again := args.PluginData.(resolvingPastPlugins); again || args.Kind == api.ResolveEntryPoint || !insideNodeModules(args.Importer) {
					return api.OnResolveResult{}, nil
				}
				resolved := build.Resolve(args.Path, api.ResolveOptions{
					Importer:   args.Importer,
					ResolveDir: args.ResolveDir,
					Kind:       args.Kind,
					PluginData: resolvingPastPlugins{},
				})
				if len(resolved.Errors) == 0 {
					return api.OnResolveResult{}, nil
				}
				importer := args.Importer
				if _, name, found := packageRoot(filepath.Dir(importer)); found {
					importer = name
				}
				u.record(fmt.Sprintf("%q imported by %s", args.Path, importer))
				return api.OnResolveResult{Path: args.Path, External: true}, nil
			})
		},
	}
}

func insideNodeModules(importer string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(importer), "/"), nodeModulesDir)
}

func (u *unresolved) record(line string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !slices.Contains(u.imports, line) {
		u.imports = append(u.imports, line)
	}
}

func (u *unresolved) report(log io.Writer, app string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if log == nil || len(u.imports) == 0 {
		return
	}
	slices.Sort(u.imports)
	fmt.Fprintf(log, "ocel: bundling %s left these imports of its dependencies unresolved, and code that reaches one fails at runtime:\n  %s\n",
		app, strings.Join(u.imports, "\n  "))
}
