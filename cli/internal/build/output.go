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

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/edge"
)

const functionsDirName = "functions"

const funcDirSuffix = ".func"

const entryFuncDirName = "index" + funcDirSuffix

var ErrNoBuildOutput = errors.New("no build output")

func ReadFunctions(projectDir string) ([]manifestbuilder.Function, error) {
	outputDir := appbuild.ArtifactRoot(projectDir)
	if _, err := os.Stat(outputDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w at %s; run `ocel build` first", ErrNoBuildOutput, appbuild.ArtifactRootDir)
		}
		return nil, err
	}
	return readFunctions(outputDir)
}

func EdgeApps(projectDir string) ([]string, error) {
	root := appbuild.ArtifactRoot(projectDir)
	names, err := builtApps(root)
	if err != nil {
		return nil, err
	}

	var apps []string
	for _, name := range names {
		desc, _, err := appbuild.ReadServeDescriptor(root, name)
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

func BuildID(projectDir, app string) (string, error) {
	desc, _, err := appbuild.ReadServeDescriptor(appbuild.ArtifactRoot(projectDir), app)
	return desc.BuildID, err
}

func builtApps(root string) ([]string, error) {
	entries, err := os.ReadDir(appbuild.AppsRoot(root))
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

func readFunctions(outputDir string) ([]manifestbuilder.Function, error) {
	apps, err := builtApps(outputDir)
	if err != nil {
		return nil, err
	}

	var functions []manifestbuilder.Function
	for _, app := range apps {
		appFunctions, err := readAppFunctions(outputDir, appbuild.AppArtifactRoot(outputDir, app))
		if err != nil {
			return nil, err
		}
		functions = append(functions, appFunctions...)
	}

	slices.SortFunc(functions, func(a, b manifestbuilder.Function) int {
		if byApp := strings.Compare(a.App, b.App); byApp != 0 {
			return byApp
		}
		return strings.Compare(a.Route, b.Route)
	})
	return functions, nil
}

func readAppFunctions(outputDir, appDir string) ([]manifestbuilder.Function, error) {
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

func readFunction(outputDir, functionsDir, funcDir string) (manifestbuilder.Function, error) {
	routeRel, err := filepath.Rel(functionsDir, funcDir)
	if err != nil {
		return manifestbuilder.Function{}, err
	}
	artifactRel, err := filepath.Rel(outputDir, funcDir)
	if err != nil {
		return manifestbuilder.Function{}, err
	}
	route := strings.TrimSuffix(filepath.ToSlash(routeRel), funcDirSuffix)

	configPath := filepath.Join(funcDir, appbuild.FunctionConfigFile)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return manifestbuilder.Function{}, fmt.Errorf("%s: missing %s", funcDir, appbuild.FunctionConfigFile)
		}
		return manifestbuilder.Function{}, err
	}

	var fc appbuild.FunctionConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return manifestbuilder.Function{}, fmt.Errorf("%s: invalid %s: %w", configPath, appbuild.FunctionConfigFile, err)
	}
	if fc.Framework.Name == "" || fc.Handler == "" || fc.App == "" {
		return manifestbuilder.Function{}, fmt.Errorf("%s: %s requires framework, handler, and app", configPath, appbuild.FunctionConfigFile)
	}

	return manifestbuilder.Function{
		Route:        route,
		Framework:    manifestbuilder.Framework{Name: fc.Framework.Name, Arch: fc.Framework.Arch},
		Handler:      fc.Handler,
		ArtifactPath: filepath.ToSlash(artifactRel),
		RouteID:      fc.ID,
		App:          fc.App,
	}, nil
}
