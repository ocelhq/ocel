package transform

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

//go:generate pnpm --dir ../../../.. exec turbo run build --filter=@platform/aws-transform-runner

//go:embed dist/runner.mjs
var runner []byte

const (
	bundleFileName = "transform.mjs"
	runnerFileName = "transform-runner.mjs"
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

	var answer struct {
		Refusal string `json:"refusal"`
		Result  *struct {
			Resources []struct {
				Name    string            `json:"name"`
				Patches Patches           `json:"patches"`
				Tags    map[string]string `json:"tags"`
			} `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &answer); err != nil {
		return nil, fmt.Errorf("decode transform result: %w", err)
	}
	if answer.Refusal != "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "transforms rejected this deploy: %s", answer.Refusal)
	}
	if answer.Result == nil {
		return nil, fmt.Errorf("the transform runner answered with neither a result nor a refusal")
	}
	decoded := answer.Result
	if len(decoded.Resources) != len(req.Resources) {
		return nil, fmt.Errorf("transforms returned %d resources for %d candidates", len(decoded.Resources), len(req.Resources))
	}

	results := make([]Result, len(decoded.Resources))
	for i, r := range decoded.Resources {
		if r.Name != req.Resources[i].Name {
			return nil, fmt.Errorf("transforms returned %q where %q was asked for", r.Name, req.Resources[i].Name)
		}
		results[i] = Result{Patches: r.Patches, Tags: r.Tags}
	}
	return results, nil
}

func (p NodePass) bundle() (string, error) {
	outDir := filepath.Join(p.Root, constants.ProjectStateDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", constants.ProjectStateDirName, err)
	}
	runnerPath := filepath.Join(outDir, runnerFileName)
	if err := os.WriteFile(runnerPath, runner, 0o644); err != nil {
		return "", fmt.Errorf("write the transform runner: %w", err)
	}
	outfile := filepath.Join(outDir, bundleFileName)

	result := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   p.entry(runnerPath),
			ResolveDir: p.Root,
			Sourcefile: "ocel-transform-entry.ts",
			Loader:     api.LoaderTS,
		},
		Bundle:   true,
		External: []string{"@pulumi/*"},
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

func (p NodePass) entry(runnerPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "import { loadModule, runEvaluate } from %q;\n", runnerPath)
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
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("open the channel transforms answer down: %w", err)
	}
	defer reader.Close()

	cmd := exec.CommandContext(ctx, "node", bundle)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = os.Stderr
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.ExtraFiles = []*os.File{writer}

	if err := cmd.Start(); err != nil {
		writer.Close()
		return nil, fmt.Errorf("run transforms: %w", err)
	}
	writer.Close()

	answered := make(chan []byte, 1)
	go func() {
		out, _ := io.ReadAll(reader)
		answered <- out
	}()
	waited := cmd.Wait()
	out := <-answered

	if len(out) > 0 {
		return out, nil
	}
	said := strings.TrimSpace(stderr.String())
	if waited == nil {
		return nil, fmt.Errorf("the transform runner exited without answering: %s", said)
	}
	if strings.Contains(said, "ERR_MODULE_NOT_FOUND") {
		return nil, providerkit.Refuse(providerkit.CodeNotReady,
			"a transform module imports a package this project has not installed, so node could not load it. `@ocel/transforms` carries `@pulumi/aws` itself: install it as a devDependency and re-run.\n%s",
			said)
	}
	return nil, fmt.Errorf("run transforms: %w\n%s", waited, said)
}
