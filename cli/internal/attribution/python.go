package attribution

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/discovery"
)

//go:embed reach.py
var pythonReachScript string

type pythonWalk struct {
	Entries map[string][]string `json:"entries"`
	Error   *struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		Message string `json:"message"`
	} `json:"error"`
}

type pythonReach struct{}

func (pythonReach) Entries(ctx context.Context, root string, app App) (map[string]Reachability, error) {
	dir := filepath.Join(root, filepath.FromSlash(app.Path))
	walked, err := walkPythonImports(ctx, app.Name, dir, pythonSearchDirs(app.Roots))
	if err != nil {
		return nil, err
	}
	if walked.Error != nil {
		file, _ := relativeToRoot(root, walked.Error.File)
		if walked.Error.Message != "" {
			return nil, fmt.Errorf("attribution: app %q: %s:%d: %s", app.Name, file, walked.Error.Line, walked.Error.Message)
		}
		return nil, &UnresolvedImportError{
			App:    app.Name,
			File:   file,
			Line:   walked.Error.Line,
			Detail: "the module it names is computed, not written out",
		}
	}

	entries := map[string]Reachability{}
	for file, closure := range walked.Entries {
		entry, inside := relativeToRoot(root, file)
		if !inside {
			continue
		}
		reached := make(map[string]bool, len(closure))
		for _, reachedFile := range closure {
			if rel, inside := relativeToRoot(root, reachedFile); inside {
				reached[rel] = true
			}
		}
		entries[entry] = func(file string) bool { return reached[file] }
	}
	return entries, nil
}

func pythonSearchDirs(roots []discovery.Root) []string {
	var dirs []string
	for _, r := range roots {
		if r.Language == discovery.Python {
			dirs = append(dirs, filepath.Dir(r.Dir))
		}
	}
	return dirs
}

func walkPythonImports(ctx context.Context, app, dir string, search []string) (pythonWalk, error) {
	program := discovery.PythonInterpreter(dir)

	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, program, append([]string{"-", dir}, search...)...)
	cmd.Stdin = strings.NewReader(pythonReachScript)
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return pythonWalk{}, fmt.Errorf("attribution: app %q: read its imports with %s: %w", app, program, err)
	}

	var walked pythonWalk
	if err := json.Unmarshal(stdout.Bytes(), &walked); err != nil {
		return pythonWalk{}, fmt.Errorf("attribution: app %q: the import walker reported nothing ocel reads", app)
	}
	return walked, nil
}
