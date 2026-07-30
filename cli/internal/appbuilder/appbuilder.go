// Package appbuilder runs the node builder embedded in the CLI over a
// project's normalized apps, then discovers the built functions by walking the
// build output. The builder is "dumb": it writes each `.func` (carrying a
// `config.json`) under `.ocel/output/apps/<app>/functions` and never reports
// anything back over stdout — this package reads those trees into the functions
// the manifest builder consumes. It resolves the builder entry from the
// project's materialized platform dist and spawns it with the user's node,
// never talking to any provider or the dev server.
package appbuilder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/platform"
)

// scratchDirName is the Ocel-internal build-artifact folder written next to
// the resolved config, shared with projectconfig and providerlocator.
const scratchDirName = ".ocel"

// outputDirName is the build output under scratchDirName; the CLI resets it
// before each build and discovers functions by walking it afterward.
const outputDirName = "output"

// appsDirName holds one subtree per app under outputDirName. Build output is
// namespaced per app — each subtree carries that app's functions, static
// assets, cache entries and routing manifest — so two apps exposing the same
// route path never write over each other.
//
// This name is a cross-process, cross-language contract with no single home:
// cli/platform/src/builder/layout.ts (APPS_DIR) writes the layout, this
// package discovers functions in it, and cloud/aws/deploy/edgeworker.go
// (appsDirName) reads each app's artifacts from it. Change one, change all three.
const appsDirName = "apps"

// functionsDirName is the subtree of an app's directory the builder writes
// `.func` artifacts into, and the only place this package looks for functions.
const functionsDirName = "functions"

// funcDirSuffix marks a directory as a function artifact.
const funcDirSuffix = ".func"

// configFileName is the metadata file the builder writes at the root of each
// `.func`.
const configFileName = "config.json"

// builderRequest is the JSON the node-builder CLI reads from stdin. ProjectRoot
// is always sent so the builder can auto-detect a single app when Apps is empty.
type builderRequest struct {
	OutDir      string     `json:"outDir"`
	ProjectRoot string     `json:"projectRoot"`
	Apps        []appInput `json:"apps"`
}

type appInput struct {
	Name       string `json:"name"`
	Cwd        string `json:"cwd"`
	Entrypoint string `json:"entrypoint,omitempty"`
	Framework  string `json:"framework,omitempty"`
}

// functionConfig is the config.json the builder writes at the root of each
// `.func`. All four fields are required: the builder and CLI ship in one npm
// release, so this is a lockstep contract with no version negotiation.
type functionConfig struct {
	Runtime   string `json:"runtime"`
	Handler   string `json:"handler"`
	Framework string `json:"framework"`
	// App names the application this function was built from — including the
	// app the builder detected when the config declared none.
	App string `json:"app"`
	// ID is the framework-native route identity (e.g. Next's "/api/documents")
	// a routing layer dispatches to. Optional: frameworks without a routing
	// layer omit it, so unlike the three fields above it is not required.
	ID string `json:"id,omitempty"`
}

// builderExec runs the builder script with the request on stdin. It is a
// package var so tests can simulate the builder (writing config.json files
// into the output) without spawning node.
var builderExec = runNode

// Build resets the project's build output and runs the node builder over it.
// The builder always runs: with no configured apps it attempts to auto-detect a
// single app at the project root, so whether there is anything to deploy is
// decided by CollectFunctions walking the output afterward, not up front. Builder
// progress and failure output are forwarded to stderr; a non-zero exit is
// surfaced as an error so callers can abort before spawning a provider.
func Build(ctx context.Context, cfg *projectconfig.Config, stderr io.Writer) error {
	outputDir := filepath.Join(cfg.Dir, scratchDirName, outputDirName)
	relOutput := filepath.Join(scratchDirName, outputDirName)

	// Clear the output so discovery is deterministic: a stale `.func` from a
	// previous build must not survive to be deployed.
	if err := os.RemoveAll(outputDir); err != nil {
		return fmt.Errorf("reset %s: %w", relOutput, err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", relOutput, err)
	}

	builderPath := platform.BuilderPath(cfg.Dir)
	if _, err := os.Stat(builderPath); err != nil {
		return fmt.Errorf("node builder not found at %s: %w", builderPath, err)
	}

	req := builderRequest{OutDir: outputDir, ProjectRoot: cfg.Dir, Apps: make([]appInput, 0, len(cfg.Apps))}
	for _, a := range cfg.Apps {
		req.Apps = append(req.Apps, appInput{
			Name:       a.Name,
			Cwd:        filepath.Join(cfg.Dir, a.Path),
			Entrypoint: a.Entrypoint,
			Framework:  a.Framework,
		})
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal build request: %w", err)
	}
	return builderExec(ctx, builderPath, platform.AdapterPath(cfg.Dir), payload, stderr)
}

// CollectFunctions returns the functions in a project's build output,
// discovered by walking .ocel/output. A missing output directory is an error; an
// output directory holding no functions is not — a fully static export
// legitimately builds none.
func CollectFunctions(projectDir string) ([]manifestbuilder.Function, error) {
	outputDir := filepath.Join(projectDir, scratchDirName, outputDirName)
	if _, err := os.Stat(outputDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no build output at %s; run `ocel build` first", filepath.Join(scratchDirName, outputDirName))
		}
		return nil, err
	}
	return collectFunctions(outputDir)
}

// BuildID reads the build id an app's build stamped into its routing manifest
// at <projectDir>/.ocel/output/apps/<app>/routing-manifest.json. It returns ""
// for an app whose framework writes no routing manifest, or whose manifest
// carries no build id: those apps have no build identity the CLI can know (the
// provider mints one deploy-side), which is a fact about the app, not an error.
func BuildID(projectDir, app string) string {
	raw, err := os.ReadFile(filepath.Join(projectDir, scratchDirName, outputDirName, appsDirName, app, "routing-manifest.json"))
	if err != nil {
		return ""
	}
	var manifest struct {
		BuildID string `json:"buildId"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return ""
	}
	return manifest.BuildID
}

// collectFunctions walks each app's <outputDir>/apps/<app>/functions subtree
// and returns one function per `*.func` directory, reading its config.json.
// Nested functions (e.g. functions/api/todos/[id].func) are supported. A
// function's name is app-qualified — the owning app, then the `.func`
// directory's path under functions/ with the suffix stripped — so two apps
// exposing the same route path stay distinct all the way to the Lambda they
// become. Its artifact_path is that directory relative to outputDir. A `.func`
// that is missing or has an invalid config.json is a hard error naming the
// file. The result is sorted by name for determinism.
func collectFunctions(outputDir string) ([]manifestbuilder.Function, error) {
	appsDir := filepath.Join(outputDir, appsDirName)
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var functions []manifestbuilder.Function
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		appFunctions, err := collectAppFunctions(outputDir, filepath.Join(appsDir, entry.Name()), entry.Name())
		if err != nil {
			return nil, err
		}
		functions = append(functions, appFunctions...)
	}

	sort.Slice(functions, func(i, j int) bool { return functions[i].Name < functions[j].Name })
	return functions, nil
}

// collectAppFunctions reads every `.func` in one app's subtree. An app that
// built no functions (a fully static export, say) contributes none.
func collectAppFunctions(outputDir, appDir, app string) ([]manifestbuilder.Function, error) {
	functionsDir := filepath.Join(appDir, functionsDirName)
	if _, err := os.Stat(functionsDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var functions []manifestbuilder.Function
	walkErr := filepath.WalkDir(functionsDir, func(dir string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || dir == functionsDir || !strings.HasSuffix(d.Name(), funcDirSuffix) {
			return nil
		}

		fn, err := readFunction(outputDir, functionsDir, dir, app)
		if err != nil {
			return err
		}
		functions = append(functions, fn)
		// A `.func` is a leaf unit; its own node_modules etc. are never functions.
		return filepath.SkipDir
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return functions, nil
}

// readFunction reads one `.func` directory's config.json into a manifest
// function. Name and artifact_path come from the directory's location, not
// from config.json.
func readFunction(outputDir, functionsDir, funcDir, app string) (manifestbuilder.Function, error) {
	routeRel, err := filepath.Rel(functionsDir, funcDir)
	if err != nil {
		return manifestbuilder.Function{}, err
	}
	artifactRel, err := filepath.Rel(outputDir, funcDir)
	if err != nil {
		return manifestbuilder.Function{}, err
	}
	name := app + "/" + strings.TrimSuffix(filepath.ToSlash(routeRel), funcDirSuffix)

	configPath := filepath.Join(funcDir, configFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return manifestbuilder.Function{}, fmt.Errorf("%s: missing %s", funcDir, configFileName)
		}
		return manifestbuilder.Function{}, err
	}

	var fc functionConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return manifestbuilder.Function{}, fmt.Errorf("%s: invalid %s: %w", configPath, configFileName, err)
	}
	if fc.Runtime == "" || fc.Handler == "" || fc.Framework == "" || fc.App == "" {
		return manifestbuilder.Function{}, fmt.Errorf("%s: %s requires runtime, handler, framework, and app", configPath, configFileName)
	}

	return manifestbuilder.Function{
		Name:         name,
		Runtime:      fc.Runtime,
		Handler:      fc.Handler,
		ArtifactPath: filepath.ToSlash(artifactRel),
		Framework:    fc.Framework,
		RouteID:      fc.ID,
		App:          fc.App,
	}, nil
}

// runNode spawns the builder. NEXT_ADAPTER_PATH is read by Next itself
// (next/dist/server/config-shared.js), reaching `next build` by env
// inheritance through the builder, so it must be set here rather than passed
// in the request.
func runNode(ctx context.Context, scriptPath, adapterPath string, request []byte, stderr io.Writer) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node not found on PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, "node", scriptPath)
	cmd.Env = append(os.Environ(), "NEXT_ADAPTER_PATH="+adapterPath)
	cmd.Stdin = bytes.NewReader(request)
	cmd.Stdout = stderr

	var capturedErr bytes.Buffer
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &capturedErr)
	} else {
		cmd.Stderr = &capturedErr
	}

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(capturedErr.String()); msg != "" {
			return fmt.Errorf("node-builder failed: %s", msg)
		}
		return fmt.Errorf("node-builder failed: %w", err)
	}
	return nil
}
