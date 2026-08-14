package appbundler

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const HandlerFile = "index.mjs"

const configFileName = "config.json"

const nativeDirName = "native"

const nodeModulesDirName = "node_modules"

const tracingHint = "set OCEL_BUILD_PREFER_TRACING=1 to build this app by tracing instead of bundling"

const banner = `import { createRequire as __ocelCreateRequire } from "node:module";` +
	`import { fileURLToPath as __ocelFileURLToPath } from "node:url";` +
	`import { dirname as __ocelPathDirname } from "node:path";` +
	`const require = __ocelCreateRequire(import.meta.url);` +
	`const __ocelFilename = __ocelFileURLToPath(import.meta.url);` +
	`const __ocelDirname = __ocelPathDirname(__ocelFilename);`

type Target struct {
	App        string
	Framework  string
	Runtime    string
	Entrypoint string
	FuncDir    string
	AppDir     string
	Log        io.Writer
}

type functionConfig struct {
	Runtime   string `json:"runtime"`
	Handler   string `json:"handler"`
	Framework string `json:"framework"`
	App       string `json:"app"`
}

func Bundle(t Target) error {
	if err := t.validate(); err != nil {
		return err
	}
	engine, err := nodeEngine(t.Runtime)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(t.FuncDir); err != nil {
		return fmt.Errorf("reset %s: %w", t.FuncDir, err)
	}
	if err := os.MkdirAll(t.FuncDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", t.FuncDir, err)
	}

	native := &addons{}
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{t.Entrypoint},
		AbsWorkingDir:     filepath.Dir(t.Entrypoint),
		Bundle:            true,
		Platform:          api.PlatformNode,
		Format:            api.FormatESModule,
		Engines:           []api.Engine{engine},
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		Outfile:           filepath.Join(t.FuncDir, HandlerFile),
		Write:             true,
		LogLevel:          api.LogLevelSilent,
		Banner:            map[string]string{"js": banner},
		Define: map[string]string{
			"__dirname":  "__ocelDirname",
			"__filename": "__ocelFilename",
		},
		Plugins: []api.Plugin{native.plugin()},
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return fmt.Errorf("bundle %s for app %q failed:\n%s", t.Entrypoint, t.App, strings.Join(msgs, "\n"))
	}
	if t.Log != nil && len(result.Warnings) > 0 {
		msgs := api.FormatMessages(result.Warnings, api.FormatMessagesOptions{Color: false, Kind: api.WarningMessage})
		fmt.Fprintf(t.Log, "ocel: bundling %s reported:\n%s\n", t.App, strings.Join(msgs, "\n"))
	}

	if err := native.copyInto(t.FuncDir); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(t.FuncDir, configFileName), functionConfig{
		Runtime:   t.Runtime,
		Handler:   HandlerFile,
		Framework: t.Framework,
		App:       t.App,
	}); err != nil {
		return err
	}

	buildID, err := artifactHash(t.FuncDir)
	if err != nil {
		return err
	}
	return writeJSON(filepath.Join(t.AppDir, edge.ServeDescriptorFile), edge.ServeDescriptor{
		Framework: t.Framework,
		BuildID:   buildID,
	})
}

func (t Target) validate() error {
	stated := []struct {
		name  string
		value string
	}{
		{"app", t.App},
		{"appDir", t.AppDir},
		{"entrypoint", t.Entrypoint},
		{"framework", t.Framework},
		{"funcDir", t.FuncDir},
		{"runtime", t.Runtime},
	}
	var missing []string
	for _, field := range stated {
		if field.value == "" {
			missing = append(missing, field.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("cannot bundle: %s not stated", strings.Join(missing, ", "))
	}
	if info, err := os.Stat(t.Entrypoint); err != nil {
		return fmt.Errorf("entrypoint %s for app %q: %w", t.Entrypoint, t.App, err)
	} else if info.IsDir() {
		return fmt.Errorf("entrypoint %s for app %q is a directory", t.Entrypoint, t.App)
	}
	return nil
}

var runtimeVersion = regexp.MustCompile(`^nodejs(\d+)(?:\.\d+)?\.x$`)

func nodeEngine(runtime string) (api.Engine, error) {
	match := runtimeVersion.FindStringSubmatch(runtime)
	if match == nil {
		return api.Engine{}, fmt.Errorf("cannot bundle for runtime %q: only nodejs<major>.x runtimes are bundled", runtime)
	}
	return api.Engine{Name: api.EngineNode, Version: match[1]}, nil
}

func writeJSON(dest string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dest, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

type addon struct {
	source string
	dest   string
}

type addons struct {
	mu     sync.Mutex
	placed []addon
}

func (a *addons) plugin() api.Plugin {
	return api.Plugin{
		Name: "ocel-native-addon",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `\.node$`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if args.Kind == api.ResolveEntryPoint {
					return api.OnResolveResult{}, nil
				}
				dest, err := a.place(args)
				if err != nil {
					return api.OnResolveResult{}, err
				}
				return api.OnResolveResult{Path: "./" + dest, External: true}, nil
			})
			build.OnResolve(api.OnResolveOptions{Filter: loaderFilter}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{}, fmt.Errorf(
					"%s reaches a native addon through %q, which finds its .node binary at run time from a path bundling cannot see; %s",
					args.Importer, args.Path, tracingHint)
			})
		},
	}
}

var addonLoaders = []string{
	"@mapbox/node-pre-gyp",
	"bindings",
	"node-gyp-build",
	"node-pre-gyp",
	"prebuild-install",
}

var loaderFilter = func() string {
	names := make([]string, 0, len(addonLoaders))
	for _, name := range addonLoaders {
		names = append(names, regexp.QuoteMeta(name))
	}
	return `^(?:` + strings.Join(names, "|") + `)(?:/|$)`
}()

func (a *addons) place(args api.OnResolveArgs) (string, error) {
	source := args.Path
	if !filepath.IsAbs(source) {
		if !strings.HasPrefix(source, ".") {
			return "", fmt.Errorf("native addon %q required by %s is not a file path; %s", args.Path, args.Importer, tracingHint)
		}
		source = filepath.Join(args.ResolveDir, source)
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("native addon %q required by %s was not found at %s; %s", args.Path, args.Importer, source, tracingHint)
	}

	dest := addonDest(source)

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, placed := range a.placed {
		if placed.dest != dest {
			continue
		}
		if placed.source == source {
			return dest, nil
		}
		return "", fmt.Errorf("native addons %s and %s both land on %s; %s", placed.source, source, dest, tracingHint)
	}
	a.placed = append(a.placed, addon{source: source, dest: dest})
	return dest, nil
}

func (a *addons) copyInto(funcDir string) error {
	a.mu.Lock()
	all := append([]addon(nil), a.placed...)
	a.mu.Unlock()
	for _, placed := range all {
		dest := filepath.Join(funcDir, filepath.FromSlash(placed.dest))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(placed.source)
		if err != nil {
			return fmt.Errorf("copy native addon %s: %w", placed.source, err)
		}
		if err := os.WriteFile(dest, data, 0o755); err != nil {
			return fmt.Errorf("copy native addon %s: %w", placed.source, err)
		}
	}
	return nil
}

func addonDest(source string) string {
	if root, name, ok := packageRoot(filepath.Dir(source)); ok {
		if rel, err := filepath.Rel(root, source); err == nil {
			return path.Join(nodeModulesDirName, name, filepath.ToSlash(rel))
		}
	}
	return path.Join(nativeDirName, filepath.Base(source))
}

func packageRoot(dir string) (string, string, bool) {
	for {
		raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err == nil {
			var pkg struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(raw, &pkg) == nil && pkg.Name != "" {
				return dir, pkg.Name, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", false
		}
		dir = parent
	}
}
