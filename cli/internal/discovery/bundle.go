package discovery

import (
	"errors"
	"fmt"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/cli/node"
)

func Bundle(configDir string, files []string, outfile string) ([]byte, error) {
	installed, err := node.Ensure()
	if err != nil {
		return nil, err
	}
	var entry strings.Builder
	fmt.Fprintf(&entry, "import { discover } from %q;\n", installed.DiscoveryRuntimePath())
	entry.WriteString("await discover([\n")
	for _, f := range files {
		fmt.Fprintf(&entry, "  () => import(%q),\n", f)
	}
	entry.WriteString("]);\n")
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
