package toolchain

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/ocelhq/ocel/pkg/appbuild"
)

const handlerFile = "index.mjs"

var engine = api.Engine{Name: api.EngineNode, Version: "24"}

const entryRouteID = "/"

const nativeDirName = "native"

const nodeModulesDir = "node_modules"

const tracingHint = "set OCEL_BUILD_PREFER_TRACING=1 to build this app by tracing instead of bundling"

const banner = `import { createRequire as __ocelCreateRequire } from "node:module";` +
	`import { fileURLToPath as __ocelFileURLToPath } from "node:url";` +
	`import { dirname as __ocelPathDirname } from "node:path";` +
	`const require = __ocelCreateRequire(import.meta.url);` +
	`const __ocelFilename = __ocelFileURLToPath(import.meta.url);` +
	`const __ocelDirname = __ocelPathDirname(__ocelFilename);`

type Target struct {
	App        string
	Framework  appbuild.Framework
	Entrypoint string
	FuncDir    string
	AppDir     string
	Log        io.Writer
}

func Bundle(ctx context.Context, t Target) error {
	if err := t.validate(); err != nil {
		return err
	}
	if err := os.RemoveAll(t.FuncDir); err != nil {
		return fmt.Errorf("reset %s: %w", t.FuncDir, err)
	}
	if err := os.MkdirAll(t.FuncDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", t.FuncDir, err)
	}

	native := &addons{arch: t.Framework.Arch}
	install := &crossInstall{arch: t.Framework.Arch}
	result := api.Build(api.BuildOptions{
		EntryPoints:       []string{t.Entrypoint},
		AbsWorkingDir:     filepath.Dir(t.Entrypoint),
		Bundle:            true,
		Platform:          api.PlatformNode,
		Format:            api.FormatESModule,
		Engines:           []api.Engine{engine},
		MinifyWhitespace:  true,
		MinifyIdentifiers: true,
		MinifySyntax:      true,
		Outfile:           filepath.Join(t.FuncDir, handlerFile),
		Write:             true,
		Metafile:          true,
		LogLevel:          api.LogLevelSilent,
		Banner:            map[string]string{"js": banner},
		Define: map[string]string{
			"__dirname":  "__ocelDirname",
			"__filename": "__ocelFilename",
		},
		Plugins: []api.Plugin{install.plugin(), native.plugin()},
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return fmt.Errorf("bundle %s for app %q failed:\n%s", t.Entrypoint, t.App, strings.Join(msgs, "\n"))
	}
	if t.Log != nil && len(result.Warnings) > 0 {
		msgs := api.FormatMessages(result.Warnings, api.FormatMessagesOptions{Color: false, Kind: api.WarningMessage})
		fmt.Fprintf(t.Log, "ocel: bundling %s reported:\n%s\n", t.App, strings.Join(msgs, "\n"))
	}
	reportRuntimeFileRisk(t.Log, t.App, result.Metafile, filepath.Dir(t.Entrypoint))

	if err := native.verify(); err != nil {
		return err
	}
	if err := native.copyInto(t.FuncDir); err != nil {
		return err
	}
	if err := install.installInto(ctx, t.App, filepath.Dir(t.Entrypoint), t.FuncDir); err != nil {
		return err
	}
	return describeArtifact(t.App, t.Framework, handlerFile, nil, t.FuncDir, t.AppDir)
}

func (t Target) validate() error {
	if err := refuseUnstated("bundle", []statedField{
		{"app", t.App},
		{"appDir", t.AppDir},
		{"entrypoint", t.Entrypoint},
		{"framework", t.Framework.Name},
		{"funcDir", t.FuncDir},
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
