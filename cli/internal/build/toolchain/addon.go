package toolchain

import (
	"encoding/binary"
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
	"github.com/ocelhq/ocel/pkg/arch"
)

type addon struct {
	source   string
	importer string
	dest     string
	pkg      string
	arch     string
	loadable bool
}

type addons struct {
	arch string

	mu     sync.Mutex
	traced []addon
}

func machineName(machine uint16) string {
	if architecture, known := arch.OfELFMachine(machine); known {
		return architecture
	}
	return fmt.Sprintf("ELF machine %#x", machine)
}

func elfMachine(source string) (uint16, bool) {
	file, err := os.Open(source)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	var header [20]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return 0, false
	}
	if string(header[:4]) != "\x7fELF" {
		return 0, false
	}
	if header[5] == 2 {
		return binary.BigEndian.Uint16(header[18:20]), true
	}
	return binary.LittleEndian.Uint16(header[18:20]), true
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
	want, known := arch.ELFMachine(a.arch)
	if !known {
		return "", fmt.Errorf("native addon %s required by %s cannot be checked: this app declares architecture %q, which nothing runs it on",
			source, args.Importer, a.arch)
	}

	root, name, inPackage := packageRoot(filepath.Dir(source))
	traced := addon{source: source, importer: args.Importer, dest: addonDest(source, root, name, inPackage), pkg: source}
	if inPackage {
		traced.pkg = root
	}
	if machine, isELF := elfMachine(source); isELF {
		traced.arch = machineName(machine)
		traced.loadable = machine == want
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for _, seen := range a.traced {
		if seen.dest != traced.dest {
			continue
		}
		if seen.source == traced.source {
			return traced.dest, nil
		}
		return "", fmt.Errorf("native addons %s and %s both land on %s; %s", seen.source, traced.source, traced.dest, tracingHint)
	}
	a.traced = append(a.traced, traced)
	return traced.dest, nil
}

func (a *addons) verify() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	serves := map[string]bool{}
	for _, traced := range a.traced {
		if traced.loadable {
			serves[traced.pkg] = true
		}
	}
	for _, traced := range a.traced {
		if traced.loadable || serves[traced.pkg] {
			continue
		}
		var told []string
		for _, other := range a.traced {
			if other.pkg == traced.pkg {
				told = append(told, describeAddon(other))
			}
		}
		return fmt.Errorf("no native addon under %s can be loaded on %s, the architecture this app declares: %s; a package that builds or fetches its addon while it installs cannot be installed for another machine, so install the app's dependencies on a linux host of the declared architecture, declare the architecture they were built for, or set \"compute\": \"container\" to install them inside the app's image",
			traced.pkg, arch.Architecture(a.arch), strings.Join(told, ", "))
	}
	return nil
}

func describeAddon(traced addon) string {
	if traced.arch == "" {
		return fmt.Sprintf("%s required by %s is not a linux ELF binary", traced.source, traced.importer)
	}
	return fmt.Sprintf("%s required by %s is built for %s", traced.source, traced.importer, traced.arch)
}

func (a *addons) copyInto(functionDir string) error {
	a.mu.Lock()
	all := append([]addon(nil), a.traced...)
	a.mu.Unlock()
	for _, placed := range all {
		if !placed.loadable {
			continue
		}
		dest := filepath.Join(functionDir, filepath.FromSlash(placed.dest))
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

func addonDest(source, root, name string, inPackage bool) string {
	if inPackage {
		if rel, err := filepath.Rel(root, source); err == nil {
			return path.Join(nodeModulesDir, name, filepath.ToSlash(rel))
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
