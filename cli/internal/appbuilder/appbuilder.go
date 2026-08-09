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
	"unicode"

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

// appInput is one app's build. Env and Folder are what make it this app's
// build rather than the project's: the builder applies them to that app's
// framework build alone, so two apps resolving one key differently are each
// built with their own value under their own binding.
type appInput struct {
	Name       string            `json:"name"`
	Cwd        string            `json:"cwd"`
	Entrypoint string            `json:"entrypoint,omitempty"`
	Framework  string            `json:"framework,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Folder     string            `json:"folder,omitempty"`
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

// builderExec runs the builder script with the request on stdin, under the
// environment Build composed. It is a package var so tests can simulate the
// builder (writing config.json files into the output) without spawning node.
var builderExec = runNode

// adapterPathEnv points the builder — and, by inheritance, `next build` itself
// — at the framework adapter in the project's materialized platform dist.
const adapterPathEnv = "NEXT_ADAPTER_PATH"

// appFolderEnv carries the variable folder the app being built binds. The SDK
// reads it to decide whether a scoped key is this app's to read
// (packages/ocel/src/env/scope.ts), so a build that does not state it is told
// it binds the project root and refuses every scoped read — including the ones
// whose values are in this very environment. cloud/aws/deploy sets the same
// name on the deployed function; this is the build-time half of it.
const appFolderEnv = "OCEL_APP_FOLDER"

// buildOwnedNames are the entries the build environment owns rather than
// carries: the two above, and the search path the builder finds node and the
// framework CLI on.
var buildOwnedNames = []string{adapterPathEnv, appFolderEnv, "PATH"}

// checkVariableNames refuses a resolved value named like something the build
// runs on. Either outcome of such a collision is wrong — honouring it breaks
// the build with an error that names nothing, ignoring it silently drops a
// value the project declared — so the collision itself is the report.
func checkVariableNames(vars map[string]string) error {
	for _, name := range buildOwnedNames {
		if _, taken := vars[name]; taken {
			return fmt.Errorf("a variable is declared as %s, which the build environment owns; rename it where it is declared", name)
		}
	}
	return nil
}

// rootAppEnv is the key envByApp carries the builder process's own environment
// under. A project that configures no apps has no name to key by — the builder
// is what detects its one app — so that app's values travel under none.
const rootAppEnv = ""

// builderEnv is the environment the node builder runs under: the CLI's own,
// then the values of the app nothing configured — exported before the build
// because that is the only moment a framework can inline one into what it
// emits — then the entries the build owns. Those go last because exec is
// last-wins, so no resolved value can repoint the builder even if one reaches
// here. A configured app's values travel in the request instead, per app.
//
// The builder process binds the project root, always written so a binding
// inherited from whatever spawned the CLI never answers for this build; only
// an app's own request entry narrows it.
func builderEnv(adapterPath string, vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := os.Environ()
	for _, key := range keys {
		env = append(env, key+"="+vars[key])
	}
	return append(env, adapterPathEnv+"="+adapterPath, appFolderEnv+"=")
}

// AppFolder is the binding a single process serving every app runs under. It
// can only state a folder every app agrees on; where two apps bind different
// ones it states the project root, which leaves an out-of-scope read the named
// error it already is rather than handing one app the other's scoped values.
// `ocel dev` spawns one child for the whole project, so this is what it can
// tell that child; a build spawns one process per app and states each app's
// own binding instead.
func AppFolder(apps []projectconfig.App) string {
	if len(apps) == 0 {
		return ""
	}
	folder := apps[0].Folder
	for _, app := range apps[1:] {
		if app.Folder != folder {
			return ""
		}
	}
	return folder
}

// Build resets the project's build output and runs the node builder over it.
// The builder always runs: with no configured apps it attempts to auto-detect a
// single app at the project root, so whether there is anything to deploy is
// decided by CollectFunctions walking the output afterward, not up front. Builder
// progress and failure output are forwarded to stderr; a non-zero exit is
// surfaced as an error so callers can abort before spawning a provider.
func Build(ctx context.Context, cfg *projectconfig.Config, envByApp map[string]map[string]string, stderr io.Writer) error {
	for _, env := range envByApp {
		if err := checkVariableNames(env); err != nil {
			return err
		}
	}

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
			Env:        envByApp[a.Name],
			Folder:     a.Folder,
		})
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal build request: %w", err)
	}
	return builderExec(ctx, builderPath, builderEnv(platform.AdapterPath(cfg.Dir), envByApp[rootAppEnv]), payload, stderr)
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

// summaryLines is how much of the tail failureSummary headlines. Two is the
// failing line plus the line that follows it, which is usually the tool
// restating the failure; more than that starts reaching back into the warnings
// the tail exists to leave behind.
const summaryLines = 2

// failureSummary picks the lines a failed build should be named by. A build
// tool warns on the way in and dies on the way out, so the cause is at the end
// of its output and the noise is at the front: taking the tail headlines the
// failure instead of whatever deprecation notice happened to print first. Lines
// carrying no letter or digit are separators and banners, dropped so they can
// never take the tail's place. Nothing is lost by summarizing — the full output
// is already forwarded to the caller's writer as it is produced.
func failureSummary(output string) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.ContainsFunc(line, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			lines = append(lines, line)
		}
	}
	if len(lines) > summaryLines {
		lines = lines[len(lines)-summaryLines:]
	}
	return strings.Join(lines, "\n")
}

// runNode spawns the builder under the environment Build composed. Everything
// the build needs travels there rather than in the request, because `next
// build` reads some of it (NEXT_ADAPTER_PATH, via
// next/dist/server/config-shared.js) by env inheritance through the builder and
// never sees the request at all.
//
// Both streams are captured, not just stderr: a builder that reports its
// failure on stdout would otherwise fail with an error carrying no detail.
// Assigning one writer to both fields is what keeps them in the order they were
// written — os/exec gives the child a single pipe when they are equal.
func runNode(ctx context.Context, scriptPath string, env []string, request []byte, stderr io.Writer) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node not found on PATH: %w", err)
	}

	var captured bytes.Buffer
	out := io.Writer(&captured)
	if stderr != nil {
		out = io.MultiWriter(stderr, &captured)
	}

	cmd := exec.CommandContext(ctx, "node", scriptPath)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(request)
	cmd.Stdout = out
	cmd.Stderr = out

	if err := cmd.Run(); err != nil {
		if summary := failureSummary(captured.String()); summary != "" {
			return fmt.Errorf("node-builder failed (%w): %s", err, summary)
		}
		return fmt.Errorf("node-builder failed: %w", err)
	}
	return nil
}
