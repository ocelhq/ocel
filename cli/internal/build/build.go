package build

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/build/toolchain"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/images"
)

type Log struct {
	Shared io.Writer
	Unit   func(app string) (log io.Writer, ended func(error))
}

func (l Log) shared() io.Writer {
	if l.Shared == nil {
		return io.Discard
	}
	return l.Shared
}

func (l Log) App(name string) (io.Writer, func(error)) {
	if l.Unit == nil {
		return l.shared(), func(error) {}
	}
	return l.Unit(name)
}

type Output struct {
	Functions []Function
	Images    map[string]string
}

type nodeRun func(ctx context.Context, scriptPath string, request []byte, log Log) error

type imageBuild func(ctx context.Context, app image.App, arch string, progress io.Writer) (image.Image, error)

type builtArchitecture func(ctx context.Context, repository, digest string) (string, error)

type tools struct {
	node         nodeRun
	image        imageBuild
	architecture builtArchitecture
}

var installed = tools{node: runNode, image: image.Build, architecture: images.BuiltArchitecture}

func Apps(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, log Log) (Output, error) {
	return installed.apps(ctx, cfg, env, archs, log)
}

func ReadPrebuilt(ctx context.Context, cfg *project.Project, archs map[string]string) (Output, error) {
	return installed.readPrebuilt(ctx, cfg, archs)
}

func (t tools) apps(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, log Log) (Output, error) {
	if err := t.functions(ctx, cfg, env, log); err != nil {
		return Output{}, err
	}
	images, err := t.images(ctx, cfg, archs, log)
	if err != nil {
		return Output{}, err
	}
	functions, err := ReadFunctions(cfg.Dir)
	if err != nil {
		return Output{}, err
	}
	return Output{Functions: functions, Images: images}, nil
}

func (t tools) functions(ctx context.Context, cfg *project.Project, envByApp map[string]map[string]string, log Log) error {
	for _, env := range envByApp {
		if err := checkVariableNames(env); err != nil {
			return err
		}
	}

	outputDir := appbuild.ArtifactRoot(cfg.Dir)
	if err := os.RemoveAll(outputDir); err != nil {
		return fmt.Errorf("reset %s: %w", appbuild.ArtifactRootDir, err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", appbuild.ArtifactRootDir, err)
	}

	apps, err := appsToBuild(cfg)
	if err != nil {
		return err
	}
	deploymentIDs, err := recordDeploymentIDs(cfg, apps)
	if err != nil {
		return err
	}

	preferTracing := os.Getenv(toolchain.PreferTracingEnv) == "1"
	var req nodeBuildRequest
	var traced []toolchain.Target
	for _, a := range FunctionApps(apps) {
		switch name := a.Framework.Name; {
		case compiledFromSource(name):
			appLog, ended := log.App(a.Name)
			err := compile(ctx, cfg, a, outputDir, appLog)
			ended(err)
			if err != nil {
				return err
			}
		case name == appbuild.FrameworkNext:
			req.Apps = append(req.Apps, nodeAppBuild{
				Framework:     appbuild.FrameworkNext,
				Name:          a.Name,
				Cwd:           filepath.Join(cfg.Dir, a.Path),
				OutputDir:     appbuild.AppArtifactRoot(outputDir, a.Name),
				DeploymentID:  deploymentIDs[a.Name],
				Folder:        a.Folder,
				Env:           envOf(cfg, envByApp, a.Name),
				EdgeKind:      string(cfg.EdgeID()),
				AllowDegraded: cfg.AllowDegraded,
			})
		case name == appbuild.FrameworkNode:
			target, err := nodeTarget(cfg, a, outputDir)
			if err != nil {
				return err
			}
			if preferTracing {
				if _, err := target.TracedHandler(); err != nil {
					return err
				}
				req.Apps = append(req.Apps, nodeAppBuild{
					Framework:  appbuild.FrameworkNode,
					Name:       a.Name,
					Cwd:        target.Source,
					Entrypoint: target.Entrypoint,
					FuncDir:    target.FuncDir,
				})
				traced = append(traced, target)
				continue
			}
			appLog, ended := log.App(a.Name)
			target.Log = appLog
			err = toolchain.Bundle(ctx, target)
			ended(err)
			if err != nil {
				return err
			}
		case a.Framework.Missing != nil:
			return a.Framework.Missing
		default:
			return fmt.Errorf("app %q: nothing in %s says what it is built with; set \"framework\" in the app config", a.Name, filepath.Join(cfg.Dir, a.Path))
		}
	}

	if len(req.Apps) > 0 {
		scriptPath := node.BuildScriptPath(cfg.Dir)
		if _, err := os.Stat(scriptPath); err != nil {
			return fmt.Errorf("the node build script is not at %s: %w", scriptPath, err)
		}
		payload, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("marshal build request: %w", err)
		}
		if err := t.node(ctx, scriptPath, payload, log); err != nil {
			return err
		}
	}
	for _, target := range traced {
		if err := toolchain.DescribeTrace(target); err != nil {
			return err
		}
	}
	return nil
}

func nodeTarget(cfg *project.Project, a project.App, outputDir string) (toolchain.Target, error) {
	source := filepath.Join(cfg.Dir, a.Path)
	entrypoint, err := toolchain.NodeEntrypoint(source, a.Entrypoint)
	if err != nil {
		return toolchain.Target{}, fmt.Errorf("app %q: %w", a.Name, err)
	}
	appDir := appbuild.AppArtifactRoot(outputDir, a.Name)
	return toolchain.Target{
		App:        a.Name,
		Framework:  appbuild.Framework{Name: appbuild.FrameworkNode, Arch: a.Framework.Arch},
		Source:     source,
		Entrypoint: entrypoint,
		FuncDir:    filepath.Join(appDir, functionsDirName, entryFuncDirName),
		AppDir:     appDir,
	}, nil
}

func envOf(cfg *project.Project, envByApp map[string]map[string]string, app string) map[string]string {
	if len(cfg.Apps) == 0 {
		return envByApp[rootAppEnv]
	}
	return envByApp[app]
}
