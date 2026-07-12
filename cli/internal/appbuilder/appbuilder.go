// Package appbuilder runs the embedded node-builder bundle over a
// project's normalized apps and returns the functions to feed into the
// manifest. It mirrors cli/internal/deploycollector: it writes the bundle
// under the project's .ocel/ dir and spawns it with the user's node, never
// talking to any provider or the dev server.
package appbuilder

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

// node-builder.mjs is the committed, self-contained JS builder bundle embedded
// into the CLI and spawned with the user's node. It is a build artifact — a
// follow-up slice replaces this embed with the tsc-built builder that now lives
// in packages/ocel/src/builder.
//
//go:embed node-builder.mjs
var builderScript []byte

// scratchDirName is the Ocel-internal build-artifact folder written next to
// the resolved config, shared with projectconfig and providerlocator.
const scratchDirName = ".ocel"

// outputDirName is the node-builder outDir under scratchDirName; artifactPaths
// in the returned functions are relative to it.
const outputDirName = "output"

// builderRequest is the JSON the node-builder CLI reads from stdin.
type builderRequest struct {
	OutDir string     `json:"outDir"`
	Apps   []appInput `json:"apps"`
}

type appInput struct {
	Name       string `json:"name"`
	Cwd        string `json:"cwd"`
	Entrypoint string `json:"entrypoint,omitempty"`
	Framework  string `json:"framework,omitempty"`
}

// builderResponse is the JSON summary the node-builder CLI writes to stdout.
type builderResponse struct {
	Functions []functionSummary `json:"functions"`
}

type functionSummary struct {
	Name         string `json:"name"`
	Runtime      string `json:"runtime"`
	Handler      string `json:"handler"`
	ArtifactPath string `json:"artifactPath"`
	Framework    string `json:"framework"`
}

// builderExec runs the builder script with the request on stdin and returns
// its stdout. It is a package var so tests can inject canned output without
// spawning node.
var builderExec = runNode

// Build writes the embedded node-builder bundle under cfg.Dir/.ocel, spawns it
// with the user's node over cfg.Apps, and returns the built functions in the
// shape manifestbuilder.Build consumes. Build progress and any failure output
// the builder writes to stderr are forwarded to stderr; a non-zero exit is
// surfaced as an error so callers can abort before spawning a provider.
func Build(ctx context.Context, cfg *projectconfig.Config, stderr io.Writer) ([]manifestbuilder.Function, error) {
	if len(cfg.Apps) == 0 {
		return nil, nil
	}

	scratchDir := filepath.Join(cfg.Dir, scratchDirName)
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", scratchDirName, err)
	}

	scriptPath := filepath.Join(scratchDir, "node-builder.mjs")
	if err := os.WriteFile(scriptPath, builderScript, 0o644); err != nil {
		return nil, fmt.Errorf("write node-builder bundle: %w", err)
	}

	req := builderRequest{
		OutDir: filepath.Join(scratchDir, outputDirName),
		Apps:   make([]appInput, 0, len(cfg.Apps)),
	}
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

	stdout, err := builderExec(ctx, scriptPath, payload, stderr)
	if err != nil {
		return nil, err
	}

	var resp builderResponse
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return nil, fmt.Errorf("parse node-builder output: %w", err)
	}

	functions := make([]manifestbuilder.Function, 0, len(resp.Functions))
	for _, f := range resp.Functions {
		functions = append(functions, manifestbuilder.Function{
			Name:         f.Name,
			Runtime:      f.Runtime,
			Handler:      f.Handler,
			ArtifactPath: f.ArtifactPath,
			Framework:    f.Framework,
		})
	}
	return functions, nil
}

func runNode(ctx context.Context, scriptPath string, request []byte, stderr io.Writer) ([]byte, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("node not found on PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, "node", scriptPath)
	cmd.Stdin = bytes.NewReader(request)

	var stdout, capturedErr bytes.Buffer
	cmd.Stdout = &stdout
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &capturedErr)
	} else {
		cmd.Stderr = &capturedErr
	}

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(capturedErr.String()); msg != "" {
			return nil, fmt.Errorf("node-builder failed: %s", msg)
		}
		return nil, fmt.Errorf("node-builder failed: %w", err)
	}

	return stdout.Bytes(), nil
}
