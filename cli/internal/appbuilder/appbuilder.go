// Package appbuilder runs the node builder that ships with the ocel npm
// package over a project's normalized apps, then discovers the built functions
// by walking the build output. The builder is "dumb": it writes each `.func`
// (carrying a `config.json`) under `.ocel/output/apps/<app>/functions` and never reports
// anything back over stdout — this package reads those trees into the functions
// the manifest builder consumes. It resolves the builder entry from
// OCEL_BUILDER_PATH (exported by the npm launcher) and spawns it with the
// user's node, never talking to any provider or the dev server.
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
// packages/ocel/src/builder/layout.ts (APPS_DIR) writes the layout, this
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

// Build resets the project's build output, runs the node builder, and returns
// the functions discovered by walking .ocel/output. The builder always runs:
// with no configured apps it attempts to auto-detect a single app at the
// project root, so whether there is anything to deploy is decided by walking
// the output afterward, not up front. Builder progress and failure output are
// forwarded to stderr; a non-zero exit is surfaced as an error so callers can
// abort before spawning a provider.
func Build(ctx context.Context, cfg *projectconfig.Config, stderr io.Writer) ([]manifestbuilder.Function, error) {
	outputDir := filepath.Join(cfg.Dir, scratchDirName, outputDirName)
	relOutput := filepath.Join(scratchDirName, outputDirName)

	// Clear the output so discovery is deterministic: a stale `.func` from a
	// previous build must not survive to be deployed.
	if err := os.RemoveAll(outputDir); err != nil {
		return nil, fmt.Errorf("reset %s: %w", relOutput, err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", relOutput, err)
	}

	builderPath := os.Getenv("OCEL_BUILDER_PATH")
	if builderPath == "" {
		return nil, fmt.Errorf("OCEL_BUILDER_PATH is not set; the ocel CLI must be run through its npm launcher")
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
		return nil, fmt.Errorf("marshal build request: %w", err)
	}
	if err := builderExec(ctx, builderPath, payload, stderr); err != nil {
		return nil, err
	}

	return collectFunctions(outputDir)
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

func runNode(ctx context.Context, scriptPath string, request []byte, stderr io.Writer) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node not found on PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, "node", scriptPath)
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
