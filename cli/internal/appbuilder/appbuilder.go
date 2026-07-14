// Package appbuilder runs the node builder that ships with the ocel npm
// package over a project's normalized apps, then discovers the built functions
// by walking the build output. The builder is "dumb": it writes each `.func`
// (carrying a `config.json`) under `.ocel/output/functions` and never reports
// anything back over stdout — this package reads that tree into the functions
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

// functionsDirName is the subtree of outputDirName the builder writes `.func`
// artifacts into, and the only place this package looks for functions.
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
// `.func`. All three fields are required: the builder and CLI ship in one npm
// release, so this is a lockstep contract with no version negotiation.
type functionConfig struct {
	Runtime   string `json:"runtime"`
	Handler   string `json:"handler"`
	Framework string `json:"framework"`
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

// collectFunctions walks <outputDir>/functions and returns one function per
// `*.func` directory, reading its config.json. Nested functions (e.g.
// functions/api/todos/[id].func) are supported: a function's name is its
// directory path under functions/ with the .func suffix stripped, and its
// artifact_path is that directory relative to outputDir. A `.func` that is
// missing or has an invalid config.json is a hard error naming the file. The
// result is sorted by name for determinism.
func collectFunctions(outputDir string) ([]manifestbuilder.Function, error) {
	functionsDir := filepath.Join(outputDir, functionsDirName)
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

		fn, err := readFunction(outputDir, functionsDir, dir)
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

	sort.Slice(functions, func(i, j int) bool { return functions[i].Name < functions[j].Name })
	return functions, nil
}

// readFunction reads one `.func` directory's config.json into a manifest
// function. Name and artifact_path come from the directory's location, not
// from config.json.
func readFunction(outputDir, functionsDir, funcDir string) (manifestbuilder.Function, error) {
	nameRel, err := filepath.Rel(functionsDir, funcDir)
	if err != nil {
		return manifestbuilder.Function{}, err
	}
	artifactRel, err := filepath.Rel(outputDir, funcDir)
	if err != nil {
		return manifestbuilder.Function{}, err
	}
	name := strings.TrimSuffix(filepath.ToSlash(nameRel), funcDirSuffix)

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
	if fc.Runtime == "" || fc.Handler == "" || fc.Framework == "" {
		return manifestbuilder.Function{}, fmt.Errorf("%s: %s requires runtime, handler, and framework", configPath, configFileName)
	}

	return manifestbuilder.Function{
		Name:         name,
		Runtime:      fc.Runtime,
		Handler:      fc.Handler,
		ArtifactPath: filepath.ToSlash(artifactRel),
		Framework:    fc.Framework,
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
