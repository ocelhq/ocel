package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/build/toolchain"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/livedir"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/redaction"
	"github.com/ocelhq/ocel/cli/internal/userconfig"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/progress"
)

type Log struct {
	Shared io.Writer
	AppLog func(ctx context.Context, app string) (appCtx context.Context, log io.Writer, ended func(error))
	Step   func(ctx context.Context, subject string, title progress.Title) (stepCtx context.Context, ended func(error))

	hidden       redaction.Values
	sharedHidden *redaction.Writer
}

func (l Log) openStep(ctx context.Context, subject string, title progress.Title) (context.Context, func(error)) {
	if l.Step == nil {
		return ctx, func(error) {}
	}
	ctx, opened := l.Step(ctx, subject, title)
	var once sync.Once
	return ctx, func(err error) {
		once.Do(func() { opened(l.hidden.HideError(err)) })
	}
}

func (l Log) step(ctx context.Context, subject string, title progress.Title, do func(context.Context) error) error {
	ctx, ended := l.openStep(ctx, subject, title)
	err := do(ctx)
	ended(err)
	return err
}

func (l Log) endingOnFirstApp(ended func(error)) Log {
	if l.AppLog == nil {
		return l
	}
	appLog := l.AppLog
	l.AppLog = func(ctx context.Context, app string) (context.Context, io.Writer, func(error)) {
		ended(nil)
		return appLog(ctx, app)
	}
	return l
}

func (l Log) shared() io.Writer {
	if l.Shared == nil {
		return io.Discard
	}
	return l.Shared
}

func (l Log) App(ctx context.Context, name string) (context.Context, io.Writer, func(error)) {
	if l.AppLog == nil {
		return ctx, l.shared(), func(error) {}
	}
	ctx, w, ended := l.AppLog(ctx, name)
	hiding := l.hidden.Writer(w)
	return ctx, hiding, func(err error) {
		_ = hiding.Flush()
		ended(l.hidden.HideError(err))
	}
}

func (l Log) hiding(values redaction.Values) Log {
	l.hidden = values
	if l.Shared != nil {
		l.sharedHidden = values.Writer(l.Shared)
		l.Shared = l.sharedHidden
	}
	return l
}

func (l Log) flushShared() {
	if l.sharedHidden != nil {
		_ = l.sharedHidden.Flush()
	}
}

func (l Log) hideLiveValues(ctx context.Context, apps []project.App, variables map[string]AppVariables) (Log, error) {
	var readable []string
	for _, a := range apps {
		live := variables[a.Name].Live
		if err := livedir.RefuseUnnamableKeys(live); err != nil {
			return l, fmt.Errorf("app %q: %w", a.Name, err)
		}
		readable = append(readable, variables[a.Name].SecretValues()...)
	}
	hidden := redaction.NewValues(readable)
	hidden.HideInTrace(ctx)
	return l.hiding(hidden), nil
}

type Output struct {
	Functions []Function
	Images    map[string]string
	BuildIDs  map[string]string
}

type nodeRun func(ctx context.Context, scriptPath string, request []byte, log Log) error

type imageBuild func(ctx context.Context, app image.App, arch string, live image.LiveValues, progress io.Writer) (image.Image, error)

type builtImageInspection func(ctx context.Context, repository, digest string) (images.ImageInspection, error)

type fileAddition func(ctx context.Context, base image.Image, slug, app, files, dst, arch string, progress io.Writer) (image.Image, error)

type tools struct {
	node        nodeRun
	buildScript func() (string, error)
	image       imageBuild
	inspection  builtImageInspection
	addFiles    fileAddition
	liveHashKey func() ([]byte, error)
}

var installed = tools{node: runNode, buildScript: installedBuildScript, image: image.Build, inspection: images.InspectBuiltImage, addFiles: image.AddFiles, liveHashKey: userconfig.EnsureLiveHashKey}

func installedBuildScript() (string, error) {
	bundle, err := node.Ensure()
	if err != nil {
		return "", err
	}
	return bundle.BuildScriptPath(), nil
}

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
	if err := t.functions(ctx, cfg, variables, archs, host, log); err != nil {
		return Output{}, err
	}
	images, err := t.images(ctx, cfg, variables, archs, workers, log)
	if err != nil {
		return Output{}, err
	}
	functions, err := ReadFunctions(cfg.Dir)
	if err != nil {
		return Output{}, err
	}
	buildIDs := make(map[string]string, len(cfg.Apps))
	for _, a := range cfg.Apps {
		if buildIDs[a.Name], err = BuildID(cfg.Dir, a.Name); err != nil {
			return Output{}, err
		}
	}
	return Output{Functions: functions, Images: images, BuildIDs: buildIDs}, nil
}

func (t tools) functions(ctx context.Context, cfg *project.Project, variables map[string]AppVariables, archs map[string]string, host Host, log Log) (err error) {
	var reading []project.App
	for _, a := range cfg.Apps {
		if !CanReadVariablesAtBuild(a) {
			continue
		}
		if err := refuseReservedNames(a.Name, variables[a.Name]); err != nil {
			return err
		}
		reading = append(reading, a)
	}
	log, err = log.hideLiveValues(ctx, reading, variables)
	if err != nil {
		return err
	}
	defer log.flushShared()
	defer func() { err = log.hidden.HideError(err) }()

	outputDir, err := buildoutput.Root(cfg.Dir)
	if err != nil {
		return err
	}
	if err := removePreviousOutput(ctx, log, cfg.Slug, outputDir); err != nil {
		return err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", buildoutput.Dir, err)
	}

	buildIDs, err := recordBuildIDs(cfg, cfg.Apps)
	if err != nil {
		return err
	}

	var liveDirs []string
	defer func() {
		for _, dir := range liveDirs {
			err = errors.Join(err, livedir.Remove(dir))
		}
	}()
	deliverValues := func(a project.App) (environment, error) {
		if !CanReadVariablesAtBuild(a) {
			return environment{}, nil
		}
		var liveDir string
		if live := variables[a.Name].Live; len(live) > 0 {
			dir, err := livedir.Write("", "ocel-live-", live)
			if err != nil {
				return environment{}, err
			}
			liveDir = dir
			liveDirs = append(liveDirs, liveDir)
		}
		return composeEnvironment(variables[a.Name], liveDir), nil
	}

	preferTracing := os.Getenv(toolchain.PreferTracingEnv) == "1"
	var req nodeBuildRequest
	var traced []toolchain.Target
	var scriptBuilt []project.App
	for _, a := range FunctionApps(cfg.Apps) {
		switch name := a.Framework; {
		case compiledFromSource(name):
			env, err := deliverValues(a)
			if err != nil {
				return err
			}
			appCtx, appLog, ended := log.App(ctx, a.Name)
			err = compile(appCtx, cfg, a, outputDir, env, appLog)
			ended(err)
			if err != nil {
				return err
			}
		case name == buildoutput.FrameworkNext:
			scriptBuilt = append(scriptBuilt, a)
			env, err := deliverValues(a)
			if err != nil {
				return err
			}
			appHost := newAppHost(host, a)
			var functionArch string
			if a.IsBuiltFromFunction() {
				functionArch = readFunctionArchitecture(a, archs)
			}
			req.Apps = append(req.Apps, nodeAppBuild{
				Unset:         env.unset,
				Framework:     buildoutput.FrameworkNext,
				Name:          a.Name,
				Cwd:           filepath.Join(cfg.Dir, a.Path),
				OutputDir:     buildoutput.AppRoot(outputDir, a.Name),
				BuildID:       buildIDs[a.Name],
				Folder:        a.Folder,
				Env:           env.set,
				AllowDegraded: edge.NeedNames(cfg.AllowDegraded),
				PublicKeys:    variables[a.Name].PublicKeys,

				EdgeRunsCode:       appHost.EdgeRunsCode,
				EdgeCacheTagLimits: newNodeCacheTagLimits(appHost.EdgeCacheTagLimits),

				MaxFunctionBytes: appHost.MaxFunctionBytes,
				Arch:             functionArch,
			})
		case name == buildoutput.FrameworkSvelteKit:
			scriptBuilt = append(scriptBuilt, a)
			env, err := deliverValues(a)
			if err != nil {
				return err
			}
			req.Apps = append(req.Apps, nodeAppBuild{
				Unset:     env.unset,
				Framework: buildoutput.FrameworkSvelteKit,
				Name:      a.Name,
				Cwd:       filepath.Join(cfg.Dir, a.Path),
				OutputDir: buildoutput.AppRoot(outputDir, a.Name),
				BuildID:   buildIDs[a.Name],
				Folder:    a.Folder,
				Env:       env.set,
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
			appCtx, appLog, ended := log.App(ctx, a.Name)
			target.Log = appLog
			err = toolchain.Bundle(appCtx, target)
			ended(err)
			if err != nil {
				return err
			}
		default:
			return &project.FrameworkUnstatedError{App: a.Name, Path: filepath.Join(cfg.Dir, a.Path)}
		}
	}

	if len(req.Apps) > 0 {
		_, loading := log.openStep(ctx, cfg.Slug, progress.Loading.Title("the build adapters for "+nameApps(req.Apps)))
		err := t.runNodeBuild(ctx, req, log.endingOnFirstApp(loading))
		loading(err)
		if err != nil {
			return err
		}
	}
	variants := toolchain.NewPlatformVariantCache()
	defer variants.Close()
	for _, target := range traced {
		if err := log.step(ctx, target.App, checkingPlatformPackages(target.App), func(ctx context.Context) error {
			return variants.InstallTraced(ctx, target.App, target.Source, target.FunctionDir, target.Framework.Arch)
		}); err != nil {
			return err
		}
		describe := func(ctx context.Context) error { return toolchain.DescribeTrace(ctx, target) }
		if target.WorkerSource != "" {
			err = log.step(ctx, target.App, buildingWorkerEntry(target.App), describe)
		} else {
			err = describe(ctx)
		}
		if err != nil {
			return err
		}
	}
	for _, a := range scriptBuilt {
		architecture := readFunctionArchitecture(a, archs)
		if err := addWorkerEntryToFunction(ctx, cfg, a, outputDir, architecture, log); err != nil {
			return err
		}
		if err := log.step(ctx, a.Name, checkingPlatformPackages(a.Name), func(ctx context.Context) error {
			return installTracedPlatformVariants(ctx, variants, cfg, a, outputDir, architecture, newAppHost(host, a).MaxFunctionBytes)
		}); err != nil {
			return err
		}
	}
	return nil
}

func removePreviousOutput(ctx context.Context, log Log, slug, outputDir string) error {
	if _, err := os.Lstat(outputDir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return log.step(ctx, slug, progress.Removing.Title("the previous build output in "+buildoutput.Dir), func(context.Context) error {
		if err := os.RemoveAll(outputDir); err != nil {
			return fmt.Errorf("reset %s: %w", buildoutput.Dir, err)
		}
		return nil
	})
}

func (t tools) runNodeBuild(ctx context.Context, req nodeBuildRequest, log Log) error {
	scriptPath, err := t.buildScript()
	if err != nil {
		return err
	}
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("the node build script is not at %s: %w", scriptPath, err)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal build request: %w", err)
	}
	return t.node(ctx, scriptPath, payload, log)
}

func nameApps(apps []nodeAppBuild) string {
	names := make([]string, 0, len(apps))
	for _, a := range apps {
		names = append(names, a.Name)
	}
	if len(names) == 1 {
		return "app " + names[0]
	}
	return "apps " + english.And(names)
}

func checkingPlatformPackages(app string) progress.Title {
	return progress.Checking.Title("the platform packages of app " + app)
}

func buildingWorkerEntry(app string) progress.Title {
	return progress.Building.Title("the worker entry of app " + app)
}

func addWorkerEntryToFunction(ctx context.Context, cfg *project.Project, a project.App, outputDir, architecture string, log Log) error {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return err
	}
	workerSource, workerResolveDir, err := discovery.RenderNodeWorkerEntry(cfg.Dir, roots)
	if err != nil {
		return fmt.Errorf("app %q: %w", a.Name, err)
	}
	if workerSource == "" {
		return nil
	}
	return log.step(ctx, a.Name, buildingWorkerEntry(a.Name), func(ctx context.Context) error {
		functions, err := readAppFunctions(outputDir, buildoutput.AppRoot(outputDir, a.Name))
		if err != nil {
			return err
		}
		if len(functions) == 0 {
			return fmt.Errorf("app %q is built with %s and ships no function to run a worker from", a.Name, a.Framework)
		}
		hosting := functions[0]
		return toolchain.BundleWorker(ctx, toolchain.Target{
			App:              a.Name,
			Framework:        buildoutput.Framework{Name: a.Framework, Arch: architecture},
			Source:           filepath.Join(cfg.Dir, a.Path),
			FunctionDir:      filepath.Join(outputDir, filepath.FromSlash(hosting.ArtifactPath)),
			WorkerSource:     workerSource,
			WorkerResolveDir: workerResolveDir,
		})
	})
}

func installTracedPlatformVariants(ctx context.Context, variants *toolchain.PlatformVariantCache, cfg *project.Project, a project.App, outputDir, architecture string, maxFunctionBytes int64) error {
	functionDirs, err := filepath.Glob(filepath.Join(buildoutput.AppRoot(outputDir, a.Name), buildoutput.FunctionsDir, "*"+buildoutput.FunctionDirSuffix))
	if err != nil {
		return err
	}
	for _, functionDir := range functionDirs {
		if err := variants.InstallTraced(ctx, a.Name, filepath.Join(cfg.Dir, a.Path), functionDir, architecture); err != nil {
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
	entrypoint, err := toolchain.NodeEntrypoint(source, a.Entrypoint)
	if err != nil {
		return toolchain.Target{}, fmt.Errorf("app %q: %w", a.Name, err)
	}
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return toolchain.Target{}, err
	}
	workerSource, workerResolveDir, err := discovery.RenderNodeWorkerEntry(cfg.Dir, roots)
	if err != nil {
		return toolchain.Target{}, fmt.Errorf("app %q: %w", a.Name, err)
	}
	appDir := buildoutput.AppRoot(outputDir, a.Name)
	return toolchain.Target{
		App:              a.Name,
		Framework:        buildoutput.Framework{Name: buildoutput.FrameworkNode, Arch: a.Architecture()},
		Source:           source,
		Entrypoint:       entrypoint,
		FunctionDir:      filepath.Join(appDir, buildoutput.FunctionsDir, buildoutput.RootFunctionDir),
		AppDir:           appDir,
		WorkerSource:     workerSource,
		WorkerResolveDir: workerResolveDir,
	}, nil
}
