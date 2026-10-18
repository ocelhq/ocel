package discovery

import (
	"errors"
	"fmt"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
)

const protocolBanner = `
const __ocelProtoPrefix = "` + nodeprotocol.Prefix + `";
function __ocelEmit(record) {
  process.stdout.write("\n" + __ocelProtoPrefix + JSON.stringify(record) + "\n");
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

func importsOf(files []string) string {
	var body strings.Builder
	body.WriteString("try {\n")
	for _, f := range files {
		fmt.Fprintf(&body, "  await import(%q);\n", f)
	}
	body.WriteString(`  await Promise.all(globalThis.__ocelRegister ?? []);
  __ocelEmit({ type: "span_end", id: "discovery", ok: true });
} catch (err) {
  __ocelFail(err);
}
`)
	return body.String()
}

func Bundle(configDir string, files []string, outfile string) ([]byte, error) {
	var entry strings.Builder
	entry.WriteString(protocolBanner)
	entry.WriteString(importsOf(files))
	bundle, err := bundleTo(configDir, outfile, "ocel-discovery-entry.ts", entry.String())
	if err != nil {
		return nil, fmt.Errorf("bundle discovery entry failed:\n%w", err)
	}
	return bundle, nil
}

func bundleTo(resolveDir, outfile, sourcefile, source string) ([]byte, error) {
	result := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   source,
			ResolveDir: resolveDir,
			Sourcefile: sourcefile,
			Loader:     api.LoaderTS,
		},
		Bundle:    true,
		Platform:  api.PlatformNode,
		Sourcemap: api.SourceMapInline,
		Format:    api.FormatESModule,
		Outfile:   outfile,
		Write:     false,
		Banner: map[string]string{
			"js": `import { createRequire as __ocelCreateRequire } from "node:module"; const require = __ocelCreateRequire(import.meta.url);`,
		},
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return nil, errors.New(strings.Join(msgs, "\n"))
	}
	if len(result.OutputFiles) != 1 {
		return nil, fmt.Errorf("esbuild produced %d files for %s, want one", len(result.OutputFiles), outfile)
	}
	return result.OutputFiles[0].Contents, nil
}
