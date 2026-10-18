package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/workdir"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
)

var reportedErrorKinds = []string{"BuildEnvError", "EnvDefinitionError"}

func recognizedErrorKinds() string {
	encoded, err := json.Marshal(reportedErrorKinds)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func evaluateTypeScript(ctx context.Context, configPath string, env environment) (evaluated []byte, err error) {
	dir := filepath.Dir(configPath)
	run := workdir.NewRun()
	defer func() { err = errors.Join(err, run.Remove()) }()
	outfile := run.Path(dir, bundleName(configPath))

	entry := fmt.Sprintf(`const kinds = %s;
try {
  const module = await import(%q);
  process.stdout.write(JSON.stringify(module.default));
} catch (error) {
  if (error instanceof Error && kinds.includes(error.name)) {
    console.error(error.name + ": " + error.message);
    process.exit(1);
  }
  throw error;
}
`, recognizedErrorKinds(), configPath)

	result := api.Build(api.BuildOptions{
		Stdin: &api.StdinOptions{
			Contents:   entry,
			ResolveDir: dir,
			Sourcefile: "ocel-config-entry.ts",
			Loader:     api.LoaderTS,
		},
		Bundle:   true,
		Platform: api.PlatformNode,
		Format:   api.FormatESModule,
		Outfile:  outfile,
		Write:    false,
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return nil, newInvalidConfigError(fmt.Errorf("%s failed to evaluate: bundle failed:\n%s", configPath, strings.Join(msgs, "\n")), "")
	}
	if len(result.OutputFiles) != 1 {
		return nil, fmt.Errorf("%s failed to evaluate: esbuild produced %d files, want one", configPath, len(result.OutputFiles))
	}
	if outfile, err = run.Write(dir, bundleName(configPath), result.OutputFiles[0].Contents); err != nil {
		return nil, err
	}

	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("%s is a TypeScript config, and node is not on PATH: %w — write %s instead, which needs no node", configPath, err, DefaultFileName)
	}

	cmd := exec.CommandContext(ctx, "node", outfile)
	cmd.Env = env.environ()
	var stdout bytes.Buffer
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := childprocess.Run(ctx, cmd); err != nil {
		if stderr.Len() > 0 {
			return nil, newInvalidConfigError(fmt.Errorf("%s failed to evaluate: node exited with error: %s", configPath, strings.TrimSpace(stderr.String())), "")
		}
		return nil, fmt.Errorf("%s failed to evaluate: run node: %w", configPath, err)
	}

	return stdout.Bytes(), nil
}

func bundleName(configPath string) string {
	target, _, ok := formOf(filepath.Base(configPath))
	if !ok || target == "" {
		return "config.mjs"
	}
	return "config." + target + ".mjs"
}
