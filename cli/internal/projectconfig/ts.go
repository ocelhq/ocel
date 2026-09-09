package projectconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"

	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/cli/internal/procgroup"
	"github.com/ocelhq/ocel/pkg/constants"
)

var reportedErrorKinds = []string{"BuildEnvError", "EnvDefinitionError"}

func recognizedErrorKinds() string {
	encoded, err := json.Marshal(reportedErrorKinds)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func readTS(ctx context.Context, configPath string) ([]byte, error) {
	dir := filepath.Dir(configPath)
	outDir := filepath.Join(dir, constants.ProjectStateDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", constants.ProjectStateDirName, err)
	}
	outfile := filepath.Join(outDir, bundleName(configPath))

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
		Write:    true,
	})
	if len(result.Errors) > 0 {
		msgs := api.FormatMessages(result.Errors, api.FormatMessagesOptions{Color: false})
		return nil, fmt.Errorf("%s failed to evaluate: bundle failed:\n%s", configPath, strings.Join(msgs, "\n"))
	}

	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("%s is a TypeScript config, and node is not on PATH: %w — write %s instead, which needs no node", configPath, err, DefaultFileName)
	}

	environment, err := configEnv(dir)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "node", outfile)
	cmd.Env = environment
	var stderr strings.Builder
	cmd.Stderr = &stderr
	procgroup.Guard(cmd)
	stdout, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%s failed to evaluate: node exited with error: %s", configPath, strings.TrimSpace(stderr.String()))
		}
		return nil, fmt.Errorf("%s failed to evaluate: run node: %w", configPath, err)
	}

	return stdout, nil
}

func configEnv(dir string) ([]string, error) {
	file, err := dotenv.Load(dir)
	if err != nil {
		return nil, err
	}

	environment := os.Environ()
	for key, value := range file.Values {
		if _, set := os.LookupEnv(key); set {
			continue
		}
		environment = append(environment, key+"="+value)
	}
	return environment, nil
}

func bundleName(configPath string) string {
	target, _, ok := formOf(filepath.Base(configPath))
	if !ok || target == "" {
		return "config.mjs"
	}
	return "config." + target + ".mjs"
}
