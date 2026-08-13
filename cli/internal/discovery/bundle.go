package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
)

const buildDirName = ".ocel"

// protocolBanner installs the nodeprotocol wire format before anything else
// runs. Discovery imports the user's own declare files as dynamic imports
// inside a try/catch (see importsAndSync below) rather than static
// `import` statements specifically so this banner's process.on handlers
// and span_start are guaranteed to execute first: a static import's
// dependencies evaluate before the importing module's own top-level code,
// regardless of source order, so a throwing declare file would otherwise
// run before the banner ever had a chance to install its handlers. It
// stays plain console.log/JSON — see the commit body for why this doesn't
// reach for a logging or tracing dependency.
const protocolBanner = `
const __ocelProtoPrefix = "` + nodeprotocol.Prefix + `";
function __ocelEmit(record) {
  process.stdout.write(__ocelProtoPrefix + JSON.stringify(record) + "\n");
}
let __ocelFailed = false;
function __ocelFail(err) {
  if (__ocelFailed) return;
  __ocelFailed = true;
  __ocelEmit({ type: "error", stage: "discovery", message: err && err.stack ? err.stack : String(err) });
  __ocelEmit({ type: "span_end", id: "discovery", ok: false });
  process.exitCode = 1;
}
process.on("uncaughtException", __ocelFail);
process.on("unhandledRejection", __ocelFail);
__ocelEmit({ type: "span_start", id: "discovery", stage: "discovery" });
`

func importsAndSync(files []string) string {
	var body strings.Builder
	body.WriteString("try {\n")
	for _, f := range files {
		fmt.Fprintf(&body, "  await import(%q);\n", f)
	}
	body.WriteString(`  await Promise.all(globalThis.__ocelRegister ?? []);

  const __ocelSyncRes = await fetch(new URL("/sync", process.env.OCEL_DEV_SERVER), { method: "POST" });
  if (!__ocelSyncRes.ok) {
    throw new Error("sync failed: " + __ocelSyncRes.status + " " + (await __ocelSyncRes.text()));
  }
  __ocelEmit({ type: "span_end", id: "discovery", ok: true });
} catch (err) {
  __ocelFail(err);
}
`)
	return body.String()
}

func Bundle(configDir string, files []string) (string, error) {
	outDir := filepath.Join(configDir, buildDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", buildDirName, err)
	}
	outfile := filepath.Join(outDir, "entry.mjs")

	var entry strings.Builder
	entry.WriteString(protocolBanner)
	entry.WriteString(importsAndSync(files))

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
