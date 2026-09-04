package attribution

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/cli/internal/discovery"
)

var unresolvableImportLogLevels = map[string]api.LogLevel{
	"unsupported-dynamic-import": api.LogLevelWarning,
	"unsupported-require-call":   api.LogLevelWarning,
}

var assetLoaders = map[string]api.Loader{
	".js":    api.LoaderJSX,
	".css":   api.LoaderEmpty,
	".scss":  api.LoaderEmpty,
	".sass":  api.LoaderEmpty,
	".less":  api.LoaderEmpty,
	".svg":   api.LoaderEmpty,
	".png":   api.LoaderEmpty,
	".jpg":   api.LoaderEmpty,
	".jpeg":  api.LoaderEmpty,
	".gif":   api.LoaderEmpty,
	".webp":  api.LoaderEmpty,
	".avif":  api.LoaderEmpty,
	".ico":   api.LoaderEmpty,
	".woff":  api.LoaderEmpty,
	".woff2": api.LoaderEmpty,
	".ttf":   api.LoaderEmpty,
	".otf":   api.LoaderEmpty,
	".node":  api.LoaderEmpty,
	".wasm":  api.LoaderEmpty,
}

func sideEffectFreeModules(app App) api.Plugin {
	return api.Plugin{
		Name: "attribution-side-effect-free-modules",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if args.PluginData == "resolving" || args.Importer == "" {
					return api.OnResolveResult{}, nil
				}
				r := build.Resolve(args.Path, api.ResolveOptions{
					Importer:   args.Importer,
					ResolveDir: args.ResolveDir,
					Kind:       args.Kind,
					PluginData: "resolving",
				})
				if len(r.Errors) > 0 {
					external := importedByDependency(args.Importer) || (app.Container && installedFromARegistry(args.Path, app.Members))
					return api.OnResolveResult{External: external}, nil
				}
				if r.External {
					return api.OnResolveResult{}, nil
				}
				return api.OnResolveResult{Path: r.Path, SideEffects: api.SideEffectsFalse}, nil
			})
		},
	}
}

func importedByDependency(importer string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(importer), "/"), "node_modules")
}

func installedFromARegistry(path string, members []string) bool {
	if path == "" || strings.HasPrefix(path, ".") || filepath.IsAbs(path) {
		return false
	}
	for _, member := range members {
		if path == member || strings.HasPrefix(path, member+"/") {
			return false
		}
	}
	return true
}

func shakenSurvivors(root string, app App) (map[string]map[string]bool, error) {
	entries, err := discovery.Discover(root, []string{app.Path})
	if err != nil {
		return nil, fmt.Errorf("attribution: list the source files of app %q: %w", app.Name, err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	result := api.Build(api.BuildOptions{
		EntryPoints:   entries,
		AbsWorkingDir: root,
		Bundle:        true,
		Platform:      api.PlatformNode,
		Format:        api.FormatESModule,
		Outdir:        filepath.Join(root, ".ocel", "attribution", app.Name),
		Write:         false,
		Metafile:      true,
		Loader:        assetLoaders,
		LogOverride:   unresolvableImportLogLevels,
		Plugins:       []api.Plugin{sideEffectFreeModules(app)},
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return nil, fmt.Errorf("attribution: cannot read the import graph of app %q:\n%s", app.Name, strings.Join(msgs, "\n"))
	}
	if err := unresolvableImport(root, app, result.Warnings); err != nil {
		return nil, err
	}

	var meta struct {
		Outputs map[string]struct {
			EntryPoint string `json:"entryPoint"`
			Inputs     map[string]struct {
				BytesInOutput int `json:"bytesInOutput"`
			} `json:"inputs"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(result.Metafile), &meta); err != nil {
		return nil, fmt.Errorf("attribution: read the build metadata of app %q: %w", app.Name, err)
	}

	survivors := make(map[string]map[string]bool, len(entries))
	for _, out := range meta.Outputs {
		entry := filepath.ToSlash(out.EntryPoint)
		if entry == "" || out.Inputs[out.EntryPoint].BytesInOutput == 0 {
			continue
		}
		kept := make(map[string]bool, len(out.Inputs))
		for input, usage := range out.Inputs {
			if usage.BytesInOutput > 0 {
				kept[filepath.ToSlash(input)] = true
			}
		}
		survivors[entry] = kept
	}
	return survivors, nil
}

func unresolvableImport(root string, app App, warnings []api.Message) error {
	for _, w := range warnings {
		if _, unresolvable := unresolvableImportLogLevels[w.ID]; !unresolvable || w.Location == nil {
			continue
		}
		file, ok := userFile(root, w.Location.File)
		if !ok {
			continue
		}
		return &UnresolvedImportError{App: app.Name, File: file, Line: w.Location.Line, Detail: w.Text}
	}
	return nil
}

func userFile(root, file string) (string, bool) {
	rel := filepath.ToSlash(file)
	if filepath.IsAbs(file) {
		var inside bool
		if rel, inside = relativeToRoot(root, file); !inside {
			return "", false
		}
	}
	return rel, !isVendored(rel)
}
