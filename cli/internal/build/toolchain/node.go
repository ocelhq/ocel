package toolchain

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/containerimage"
)

const handlerFile = "index.mjs"

var engine = api.Engine{Name: api.EngineNode, Version: "24"}

const nativeDirName = "native"

const nodeModulesDir = "node_modules"

const PreferTracingEnv = "OCEL_BUILD_PREFER_TRACING"

const tracingHint = "set " + PreferTracingEnv + "=1 to build this app by tracing instead of bundling"

func NodeEntrypoint(source, entrypoint string) (string, error) {
	path := filepath.FromSlash(entrypoint)
	if !filepath.IsAbs(path) {
		path = filepath.Join(source, path)
	}
	if !regularFile(path) {
		return "", fmt.Errorf("entrypoint %q not found in %s", entrypoint, source)
	}
	return path, nil
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

const banner = `import { createRequire as __ocelCreateRequire } from "node:module";` +
	`import { fileURLToPath as __ocelFileURLToPath } from "node:url";` +
	`import { dirname as __ocelPathDirname } from "node:path";` +
	`const require = __ocelCreateRequire(import.meta.url);` +
	`const __ocelFilename = __ocelFileURLToPath(import.meta.url);` +
	`const __ocelDirname = __ocelPathDirname(__ocelFilename);`

type Target struct {
	App         string
	Framework   buildoutput.Framework
	Source      string
	Entrypoint  string
	FunctionDir string
	AppDir      string
	Log         io.Writer

	WorkerSource     string
	WorkerResolveDir string
}

func Bundle(ctx context.Context, t Target) error {
	if err := t.validate(); err != nil {
		return err
	}
	if err := os.RemoveAll(t.FunctionDir); err != nil {
		return fmt.Errorf("reset %s: %w", t.FunctionDir, err)
	}
	if err := os.MkdirAll(t.FunctionDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", t.FunctionDir, err)
	}

	native := &addons{arch: t.Framework.Arch}
	install := &crossInstall{arch: t.Framework.Arch}
	missing := &unresolved{}
	options := bundleOptions(filepath.Dir(t.Entrypoint), filepath.Join(t.FunctionDir, handlerFile), install, native, missing)
	options.EntryPoints = []string{t.Entrypoint}
	result := api.Build(options)
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return fmt.Errorf("bundle %s for app %q failed:\n%s", t.Entrypoint, t.App, strings.Join(msgs, "\n"))
	}
	t.reportWarnings(result)
	reportRuntimeFileRisk(t.Log, t.App, result.Metafile, filepath.Dir(t.Entrypoint))
	var worker []string
	if t.WorkerSource != "" {
		if err := t.bundleWorkerEntry(install, native, missing); err != nil {
			return err
		}
		worker = containerimage.NodeWorkerEntry.ArtifactCommand()
	}
	missing.report(t.Log, t.App)

	if err := native.verify(); err != nil {
		return err
	}
	if err := native.copyInto(t.FunctionDir); err != nil {
		return err
	}
	if err := install.installInto(ctx, t.App, filepath.Dir(t.Entrypoint), t.FunctionDir); err != nil {
		return err
	}
	return describeArtifact(t.App, t.Framework, handlerFile, nil, worker, t.FunctionDir, t.AppDir)
}

func (t Target) bundleWorkerEntry(install *crossInstall, native *addons, missing *unresolved) error {
	options := bundleOptions(t.WorkerResolveDir, filepath.Join(t.FunctionDir, containerimage.NodeWorkerEntry.ArtifactFile), install, native, missing)
	options.Stdin = &api.StdinOptions{Contents: t.WorkerSource, ResolveDir: t.WorkerResolveDir, Sourcefile: "ocel-worker-entry.ts", Loader: api.LoaderTS}
	result := api.Build(options)
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return fmt.Errorf("bundle the worker entry for app %q failed:\n%s", t.App, strings.Join(msgs, "\n"))
	}
	t.reportWarnings(result)
	return nil
}

func BundleWorker(ctx context.Context, t Target) error {
	if t.WorkerSource == "" {
		return nil
	}
	if err := refuseUnstated("bundle a worker", []statedField{
		{"app", t.App},
		{"source", t.Source},
		{"functionDir", t.FunctionDir},
		{"workerResolveDir", t.WorkerResolveDir},
	}); err != nil {
		return err
	}
	native := &addons{arch: t.Framework.Arch}
	install := &crossInstall{arch: t.Framework.Arch}
	missing := &unresolved{}
	if err := t.bundleWorkerEntry(install, native, missing); err != nil {
		return err
	}
	missing.report(t.Log, t.App)
	if err := native.verify(); err != nil {
		return err
	}
	if err := native.copyInto(t.FunctionDir); err != nil {
		return err
	}
	if err := install.installInto(ctx, t.App, t.Source, t.FunctionDir); err != nil {
		return err
	}
	return nameWorker(t.FunctionDir, containerimage.NodeWorkerEntry.ArtifactCommand())
}

func bundleOptions(workingDir, outfile string, install *crossInstall, native *addons, missing *unresolved) api.BuildOptions {
	return api.BuildOptions{
		AbsWorkingDir:     workingDir,
		Bundle:            true,
		Platform:          api.PlatformNode,
		Format:            api.FormatESModule,
		Engines:           []api.Engine{engine},
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		KeepNames:         true,
		MinifySyntax:      true,
		Outfile:           outfile,
		Write:             true,
		Metafile:          true,
		LogLevel:          api.LogLevelSilent,
		Banner:            map[string]string{"js": banner},
		Define: map[string]string{
			"__dirname":  "__ocelDirname",
			"__filename": "__ocelFilename",
		},
		Plugins: []api.Plugin{install.plugin(), native.plugin(), missing.plugin()},
	}
}

func (t Target) reportWarnings(result api.BuildResult) {
	if t.Log != nil && len(result.Warnings) > 0 {
		msgs := api.FormatMessages(result.Warnings, api.FormatMessagesOptions{Color: false, Kind: api.WarningMessage})
		fmt.Fprintf(t.Log, "ocel: bundling %s reported:\n%s\n", t.App, strings.Join(msgs, "\n"))
	}
}

func (t Target) validate() error {
	if err := refuseUnstated("bundle", []statedField{
		{"app", t.App},
		{"appDir", t.AppDir},
		{"entrypoint", t.Entrypoint},
		{"framework", t.Framework.Name},
		{"functionDir", t.FunctionDir},
	}); err != nil {
		return err
	}
	if info, err := os.Stat(t.Entrypoint); err != nil {
		return fmt.Errorf("entrypoint %s for app %q: %w", t.Entrypoint, t.App, err)
	} else if info.IsDir() {
		return fmt.Errorf("entrypoint %s for app %q is a directory", t.Entrypoint, t.App)
	}
	return nil
}
