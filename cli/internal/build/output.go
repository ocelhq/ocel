package build

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
)

const functionsDirName = "functions"

const functionDirSuffix = ".func"

const entryFunctionDirName = "index" + functionDirSuffix

var ErrNoBuildOutput = errors.New("no build output")

type Function struct {
	App          string
	Route        string
	RouteID      string
	Framework    buildoutput.Framework
	EntryFile    string
	ArtifactPath string
}

func ReadFunctions(projectDir string) ([]Function, error) {
	outputDir, err := buildoutput.Root(projectDir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(outputDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w at %s; run `ocel build` first", ErrNoBuildOutput, buildoutput.Dir)
		}
		return nil, err
	}
	return readFunctions(outputDir)
}

func EdgeApps(projectDir string) ([]string, error) {
	root, err := buildoutput.Root(projectDir)
	if err != nil {
		return nil, err
	}
	names, err := builtApps(root)
	if err != nil {
		return nil, err
	}

	var apps []string
	for _, name := range names {
		desc, _, err := buildoutput.ReadServeDescriptor(root, name)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(edge.CodeNeeds(), func(need edge.Need) bool {
			_, ok := desc.Needs[need]
			return ok
		}) {
			apps = append(apps, name)
		}
	}
	return apps, nil
}

func ServeBuildID(projectDir, app string) (string, error) {
	root, err := buildoutput.Root(projectDir)
	if err != nil {
		return "", err
	}
	desc, _, err := buildoutput.ReadServeDescriptor(root, app)
	return desc.BuildID, err
}

func builtApps(root string) ([]string, error) {
	entries, err := os.ReadDir(buildoutput.AppsRoot(root))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var apps []string
	for _, entry := range entries {
		if entry.IsDir() {
			apps = append(apps, entry.Name())
		}
	}
	return apps, nil
}

func readFunctions(outputDir string) ([]Function, error) {
	apps, err := builtApps(outputDir)
	if err != nil {
		return nil, err
	}

	var functions []Function
	for _, app := range apps {
		appFunctions, err := readAppFunctions(outputDir, buildoutput.AppRoot(outputDir, app))
		if err != nil {
			return nil, err
		}
		functions = append(functions, appFunctions...)
	}

	slices.SortFunc(functions, func(a, b Function) int {
		if byApp := strings.Compare(a.App, b.App); byApp != 0 {
			return byApp
		}
		return strings.Compare(a.Route, b.Route)
	})
	return functions, nil
}

func readAppFunctions(outputDir, appDir string) ([]Function, error) {
	functionsDir := filepath.Join(appDir, functionsDirName)
	if _, err := os.Stat(functionsDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var functions []Function
	walkErr := filepath.WalkDir(functionsDir, func(dir string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || dir == functionsDir || !strings.HasSuffix(d.Name(), functionDirSuffix) {
			return nil
		}

		fn, err := readFunction(outputDir, functionsDir, dir)
		if err != nil {
			return err
		}
		functions = append(functions, fn)
		return filepath.SkipDir
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return functions, nil
}

func readFunction(outputDir, functionsDir, functionDir string) (Function, error) {
	routeRel, err := filepath.Rel(functionsDir, functionDir)
	if err != nil {
		return Function{}, err
	}
	artifactRel, err := filepath.Rel(outputDir, functionDir)
	if err != nil {
		return Function{}, err
	}
	route := strings.TrimSuffix(filepath.ToSlash(routeRel), functionDirSuffix)

	configPath := filepath.Join(functionDir, buildoutput.FunctionDescriptorFile)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Function{}, fmt.Errorf("%s: missing %s", functionDir, buildoutput.FunctionDescriptorFile)
		}
		return Function{}, err
	}

	var fc buildoutput.FunctionDescriptor
	if err := json.Unmarshal(data, &fc); err != nil {
		return Function{}, fmt.Errorf("%s: invalid %s: %w", configPath, buildoutput.FunctionDescriptorFile, err)
	}
	if fc.Framework.Name == "" || fc.EntryFile == "" || fc.App == "" {
		return Function{}, fmt.Errorf("%s: %s requires framework, entryFile, and app", configPath, buildoutput.FunctionDescriptorFile)
	}

	return Function{
		Route:        route,
		Framework:    fc.Framework,
		EntryFile:    fc.EntryFile,
		ArtifactPath: filepath.ToSlash(artifactRel),
		RouteID:      fc.ID,
		App:          fc.App,
	}, nil
}
