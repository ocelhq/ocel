package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// buildDirName is the Ocel-internal build-artifact folder, gitignored.
const buildDirName = ".ocel"

// syncScript awaits every registration promise collected by the SDK's
// defer() during import, then POSTs /sync to signal discovery is complete.
// It throws (failing the node process, and so the CLI's discovery step) if
// the dev server rejects the sync.
const syncScript = `
await Promise.all(globalThis.__ocelRegister ?? []);

const __ocelSyncRes = await fetch(new URL("/sync", process.env.OCEL_DEV_SERVER), { method: "POST" });
if (!__ocelSyncRes.ok) {
  throw new Error("sync failed: " + __ocelSyncRes.status + " " + (await __ocelSyncRes.text()));
}
`

// Bundle generates a single entrypoint that side-effect-imports every file
// in files, awaits collected resource registrations, and posts /sync to
// OCEL_DEV_SERVER. It bundles the result with esbuild and writes it under
// configDir/.ocel, returning the path to run with node.
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
		Bundle:   true,
		Platform: api.PlatformNode,
		Format:   api.FormatESModule,
		Outfile:  outfile,
		Write:    true,
		// Bundled CJS dependencies (e.g. node-postgres) load node builtins
		// through require calls esbuild leaves in place; in ESM output those
		// throw "Dynamic require of ... is not supported" unless a real
		// createRequire-backed require is in scope.
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
