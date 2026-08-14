package transform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

const (
	scratchDirName = ".ocel"
	bundleFileName = "transform.mjs"
	runnerModule   = "@ocel/provider-aws/transform/run"
)

type NodePass struct {
	Root    string
	Modules []string
}

func (p NodePass) Evaluate(ctx context.Context, req Request) ([]Result, error) {
	if len(p.Modules) == 0 || len(req.Resources) == 0 {
		return nil, nil
	}

	bundle, err := p.bundle()
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode transform request: %w", err)
	}

	out, err := runNode(ctx, bundle, payload)
	if err != nil {
		return nil, err
	}

	var decoded struct {
		Resources []struct {
			Name     string            `json:"name"`
			Surfaces Surfaces          `json:"surfaces"`
			Tags     map[string]string `json:"tags"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		return nil, fmt.Errorf("decode transform result: %w", err)
	}
	if len(decoded.Resources) != len(req.Resources) {
		return nil, fmt.Errorf("transforms returned %d resources for %d candidates", len(decoded.Resources), len(req.Resources))
	}

	results := make([]Result, len(decoded.Resources))
	for i, r := range decoded.Resources {
		if r.Name != req.Resources[i].Name {
			return nil, fmt.Errorf("transforms returned %q where %q was asked for", r.Name, req.Resources[i].Name)
		}
		results[i] = Result{Surfaces: r.Surfaces, Tags: r.Tags}
	}
	return results, nil
}

func (p NodePass) bundle() (string, error) {
	outDir := filepath.Join(p.Root, scratchDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", scratchDirName, err)
	}
	outfile := filepath.Join(outDir, bundleFileName)

	result := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   p.entry(),
			ResolveDir: p.Root,
			Sourcefile: "ocel-transform-entry.ts",
			Loader:     api.LoaderTS,
		},
		Bundle:   true,
		Platform: api.PlatformNode,
		Format:   api.FormatESModule,
		Outfile:  outfile,
		Write:    true,
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return "", fmt.Errorf("bundle transforms failed:\n%s", strings.Join(msgs, "\n"))
	}
	return outfile, nil
}

func (p NodePass) entry() string {
	var b strings.Builder
	fmt.Fprintf(&b, "import { loadModule, runEvaluate } from %q;\n", runnerModule)
	for i, module := range p.Modules {
		fmt.Fprintf(&b, "import m%d from %q;\n", i, p.resolve(module))
	}
	b.WriteString("await runEvaluate([")
	for i, module := range p.Modules {
		fmt.Fprintf(&b, "loadModule(%q, m%d),", module, i)
	}
	b.WriteString("]);\n")
	return b.String()
}

func (p NodePass) resolve(module string) string {
	if filepath.IsAbs(module) {
		return module
	}
	resolved := filepath.Join(p.Root, filepath.FromSlash(module))
	if strings.HasPrefix(module, ".") {
		return resolved
	}
	if _, err := os.Stat(resolved); err == nil {
		return resolved
	}
	return module
}

func runNode(ctx context.Context, bundle string, payload []byte) ([]byte, error) {
	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("transforms need node on PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, "node", bundle)
	cmd.Stdin = bytes.NewReader(payload)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("transforms rejected this deploy: %s", strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("run transforms: %w", err)
	}
	return out, nil
}
