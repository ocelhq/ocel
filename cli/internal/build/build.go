package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/build/toolchain"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/livedir"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/redaction"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
)

type Log struct {
	Shared io.Writer
	AppLog func(app string) (log io.Writer, ended func(error))

	hidden redaction.Values
}

func (l Log) shared() io.Writer {
	if l.Shared == nil {
		return io.Discard
	}
	return l.Shared
}

func (l Log) App(name string) (io.Writer, func(error)) {
	if l.AppLog == nil {
		return l.shared(), func(error) {}
	}
	w, ended := l.AppLog(name)
	return l.hidden.Writer(w), ended
}

func (l Log) hiding(values redaction.Values) Log {
	l.hidden = values
	if l.Shared != nil {
		l.Shared = values.Writer(l.Shared)
	}
	return l
}

type Output struct {
	Functions []Function
	Images    map[string]string
}

type nodeRun func(ctx context.Context, scriptPath string, request []byte, log Log) error

type imageBuild func(ctx context.Context, app image.App, arch string, progress io.Writer) (image.Image, error)

type builtArchitecture func(ctx context.Context, repository, digest string) (string, error)

type fileAddition func(ctx context.Context, base image.Image, slug, app, files, dst, arch string, progress io.Writer) (image.Image, error)

type tools struct {
	node         nodeRun
	image        imageBuild
	architecture builtArchitecture
	addFiles     fileAddition
}

var installed = tools{node: runNode, image: image.Build, architecture: images.BuiltArchitecture, addFiles: image.AddFiles}

func Apps(ctx context.Context, cfg *project.Project, variables map[string]AppVariables, archs map[string]string, workers HostedWorkers, host Host, log Log) (Output, error) {
	return installed.apps(ctx, cfg, variables, archs, workers, host, log)
}

func ReadPrebuilt(ctx context.Context, cfg *project.Project, archs map[string]string) (Output, error) {
	return installed.readPrebuilt(ctx, cfg, archs)
}

func (t tools) apps(ctx context.Context, cfg *project.Project, variables map[string]AppVariables, archs map[string]string, workers HostedWorkers, host Host, log Log) (Output, error) {
	if unresolved := cfg.UnresolvedApps(); len(unresolved) > 0 {
		return Output{}, fmt.Errorf("the build reached %s with no compute resolved, and an app is built for the compute it runs on", english.And(english.Quoted(unresolved)))
	}
	if err := t.functions(ctx, cfg, variables, host, log); err != nil {
		return Output{}, err
	}
	images, err := t.images(ctx, cfg, archs, workers, log)
	if err != nil {
		return Output{}, err
	}
	functions, err := ReadFunctions(cfg.Dir)
	if err != nil {
		return Output{}, err
	}
	return Output{Functions: functions, Images: images}, nil
}

func (t tools) functions(ctx context.Context, cfg *project.Project, variables map[string]AppVariables, host Host, log Log) (err error) {
	var readable []string
	for _, a := range cfg.Apps {
		if !CanReadVariablesAtBuild(a) {
			continue
		}
		readable = append(readable, slices.Collect(maps.Values(variables[a.Name].Live))...)
		if err := checkVariableNames(variables[a.Name].Env); err != nil {
			return err
		}
		if err := checkVariableNames(variables[a.Name].Live); err != nil {
			return err
		}
		if err := livedir.RefuseUnnamableKeys(variables[a.Name].Live); err != nil {
			return fmt.Errorf("app %q: %w", a.Name, err)
		}
	}
	hidden := redaction.NewValues(readable)
	log = log.hiding(hidden)
	defer func() { err = hidden.HideError(err) }()

	if err := RefuseNextFunctionsWithoutRuntimeDir(cfg, host); err != nil {
		return err
	}
	if err := RefuseNextFunctionsWithOwnAdapter(cfg); err != nil {
		return err
	}

	outputDir, err := buildoutput.Root(cfg.Dir)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return fmt.Errorf("reset %s: %w", buildoutput.Dir, err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", buildoutput.Dir, err)
	}

	deploymentIDs, err := recordDeploymentIDs(cfg, cfg.Apps)
	if err != nil {
		return err
	}

	var liveDirs []string
	defer func() {
		for _, dir := range liveDirs {
			err = errors.Join(err, livedir.Remove(dir))
		}
	}()
	environment := func(a project.App) (env map[string]string, unset []string, err error) {
		if !CanReadVariablesAtBuild(a) {
			return nil, nil, nil
		}
		var liveDir string
		if live := variables[a.Name].Live; len(live) > 0 {
			liveDir, err = livedir.Write("", "ocel-live-", live)
			if err != nil {
				return nil, nil, err
			}
			liveDirs = append(liveDirs, liveDir)
		}
		env, unset = environmentOf(variables[a.Name], liveDir)
		return env, unset, nil
	}

	preferTracing := os.Getenv(toolchain.PreferTracingEnv) == "1"
	var req nodeBuildRequest
	var traced []toolchain.Target
	var nextApps []project.App
	for _, a := range FunctionApps(cfg.Apps) {
		switch name := a.Framework(); {
		case compiledFromSource(name):
			env, unset, err := environment(a)
			if err != nil {
				return err
			}
			appLog, ended := log.App(a.Name)
			err = compile(ctx, cfg, a, outputDir, env, unset, appLog)
			ended(hidden.HideError(err))
			if err != nil {
				return err
			}
		case name == buildoutput.FrameworkNext:
			nextApps = append(nextApps, a)
			env, unset, err := environment(a)
			if err != nil {
				return err
			}
			req.Apps = append(req.Apps, nodeAppBuild{
				Unset:         unset,
				Framework:     buildoutput.FrameworkNext,
				Name:          a.Name,
				Cwd:           filepath.Join(cfg.Dir, a.Path),
				OutputDir:     buildoutput.AppRoot(outputDir, a.Name),
				DeploymentID:  deploymentIDs[a.Name],
				Folder:        a.Folder,
				Env:           env,
				EdgeKind:      string(cfg.EdgeKind()),
				AllowDegraded: edge.NeedNames(cfg.AllowDegraded),

				NextRuntimeDir:         host.NextRuntimeDir,
				MaxFunctionBytes:       host.MaxFunctionBytes,
				NextRefreshesByRequest: host.NextRefreshesByRequest,
			})
		case name == buildoutput.FrameworkNode:
			target, err := nodeTarget(cfg, a, outputDir)
			if err != nil {
				return err
			}
			if preferTracing {
				if _, err := target.TracedHandler(); err != nil {
					return err
				}
				req.Apps = append(req.Apps, nodeAppBuild{
					Framework:   buildoutput.FrameworkNode,
					Name:        a.Name,
					Cwd:         target.Source,
					Entrypoint:  target.Entrypoint,
					FunctionDir: target.FunctionDir,
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
	variants := toolchain.NewPlatformVariantCache()
	defer variants.Close()
	for _, target := range traced {
		if err := variants.InstallTraced(ctx, target.App, target.Source, target.FunctionDir, target.Framework.Arch); err != nil {
			return err
		}
		if err := toolchain.DescribeTrace(target); err != nil {
			return err
		}
	}
	for _, a := range nextApps {
		if err := installNextPlatformVariants(ctx, variants, cfg, a, outputDir, host.MaxFunctionBytes); err != nil {
			return err
		}
	}
	return nil
}

func installNextPlatformVariants(ctx context.Context, variants *toolchain.PlatformVariantCache, cfg *project.Project, a project.App, outputDir string, maxFunctionBytes int64) error {
	functionDirs, err := filepath.Glob(filepath.Join(buildoutput.AppRoot(outputDir, a.Name), functionsDirName, "*"+functionDirSuffix))
	if err != nil {
		return err
	}
	for _, functionDir := range functionDirs {
		if err := variants.InstallTraced(ctx, a.Name, filepath.Join(cfg.Dir, a.Path), functionDir, a.Architecture()); err != nil {
			return err
		}
		if maxFunctionBytes > 0 {
			size, err := dirBytes(functionDir)
			if err != nil {
				return err
			}
			if size > maxFunctionBytes {
				return fmt.Errorf("app %q: function %s is %d bytes once its platform variants are installed, over the %d-byte budget its provider sets for one function", a.Name, filepath.Base(functionDir), size, maxFunctionBytes)
			}
		}
	}
	return nil
}

func dirBytes(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func nodeTarget(cfg *project.Project, a project.App, outputDir string) (toolchain.Target, error) {
	source := filepath.Join(cfg.Dir, a.Path)
	entrypoint, err := toolchain.NodeEntrypoint(source, a.Serverless.Entrypoint)
	if err != nil {
		return toolchain.Target{}, fmt.Errorf("app %q: %w", a.Name, err)
	}
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return toolchain.Target{}, err
	}
	workerSource, workerResolveDir, err := discovery.NodeWorkerSource(roots)
	if err != nil {
		return toolchain.Target{}, fmt.Errorf("app %q: %w", a.Name, err)
	}
	appDir := buildoutput.AppRoot(outputDir, a.Name)
	return toolchain.Target{
		App:              a.Name,
		Framework:        buildoutput.Framework{Name: buildoutput.FrameworkNode, Arch: a.Architecture()},
		Source:           source,
		Entrypoint:       entrypoint,
		FunctionDir:      filepath.Join(appDir, functionsDirName, entryFunctionDirName),
		AppDir:           appDir,
		WorkerSource:     workerSource,
		WorkerResolveDir: workerResolveDir,
	}, nil
}
