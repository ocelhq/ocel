package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const buildDirName = ".ocel"

const syncScript = `
await Promise.all(globalThis.__ocelRegister ?? []);

const __ocelSyncRes = await fetch(new URL("/sync", process.env.OCEL_DEV_SERVER), { method: "POST" });
if (!__ocelSyncRes.ok) {
  throw new Error("sync failed: " + __ocelSyncRes.status + " " + (await __ocelSyncRes.text()));
}
`

func Bundle(configDir string, files []string) (string, error) {
	outDir := filepath.Join(configDir, buildDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", buildDirName, err)
	}
	outfile := filepath.Join(outDir, "entry.mjs")

	var entry strings.Builder
	for _, f := range files {
		fmt.Fprintf(&entry, "import %q;\n", f)
	}
	entry.WriteString(syncScript)

	result := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   entry.String(),
			ResolveDir: configDir,
			Sourcefile: "ocel-discovery-entry.ts",
			Loader:     api.LoaderTS,
		},
		Bundle:    true,
		Platform:  api.PlatformNode,
		Sourcemap: api.SourceMapInline,
		Format:    api.FormatESModule,
		Outfile:   outfile,
		Write:     true,
		Banner: map[string]string{
			"js": `import { createRequire as __ocelCreateRequire } from "node:module"; const require = __ocelCreateRequire(import.meta.url);`,
		},
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return "", fmt.Errorf("bundle discovery entry failed:\n%s", strings.Join(msgs, "\n"))
	}

	return outfile, nil
}
