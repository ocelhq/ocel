package toolchain

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/english"
)

const (
	nestCLIFile          = "nest-cli.json"
	nestBuildTSConfig    = "tsconfig.build.json"
	tsconfigFile         = "tsconfig.json"
	nestDefaultOutDir    = "dist"
	nestDefaultSource    = "src"
	nestDefaultEntryFile = "main"
)

type NestBuild struct {
	App        string
	Source     string
	Command    []string
	Entrypoint string
	Log        io.Writer
}

type nestCLI struct {
	SourceRoot      string `json:"sourceRoot"`
	EntryFile       string `json:"entryFile"`
	CompilerOptions struct {
		TSConfigPath string `json:"tsConfigPath"`
	} `json:"compilerOptions"`
}

type tsconfig struct {
	Extends         json.RawMessage `json:"extends"`
	CompilerOptions struct {
		OutDir string `json:"outDir"`
	} `json:"compilerOptions"`
}

func BuildNest(ctx context.Context, b NestBuild) (string, error) {
	if err := refuseUnstated("build a NestJS app", []statedField{
		{"app", b.App},
		{"source", b.Source},
	}); err != nil {
		return "", err
	}
	cmd := exec.Command(b.Command[0], b.Command[1:]...)
	cmd.Dir = b.Source
	cmd.Env = append(os.Environ(), "NODE_ENV=production")
	var said bytes.Buffer
	cmd.Stdout = &said
	cmd.Stderr = &said
	if err := childprocess.Run(ctx, cmd); err != nil {
		return "", fmt.Errorf("build app %q with %q in %s (%w):\n%s", b.App, strings.Join(b.Command, " "), b.Source, err, said.String())
	}
	if b.Log != nil && strings.TrimSpace(said.String()) != "" {
		fmt.Fprintf(b.Log, "ocel: building %s reported:\n%s\n", b.App, strings.TrimRight(said.String(), "\n"))
	}

	if b.Entrypoint != "" {
		return NodeEntrypoint(b.Source, b.Entrypoint)
	}
	candidates, err := nestCompiledEntries(b.Source)
	if err != nil {
		return "", fmt.Errorf("app %q: %w", b.App, err)
	}
	for _, candidate := range candidates {
		if regularFile(filepath.Join(b.Source, candidate)) {
			return filepath.Join(b.Source, candidate), nil
		}
	}
	return "", fmt.Errorf("app %q built, and left no compiled entry at %s: set \"entrypoint\" to the file its build writes the server to",
		b.App, english.Or(english.Quoted(slashed(candidates))))
}

func nestCompiledEntries(source string) ([]string, error) {
	var cli nestCLI
	if _, err := readJSONC(filepath.Join(source, nestCLIFile), &cli); err != nil {
		return nil, err
	}
	sourceRoot := cmp.Or(cli.SourceRoot, nestDefaultSource)
	entry := cmp.Or(cli.EntryFile, nestDefaultEntryFile) + ".js"

	config := cli.CompilerOptions.TSConfigPath
	if config == "" {
		config = tsconfigFile
		if regularFile(filepath.Join(source, nestBuildTSConfig)) {
			config = nestBuildTSConfig
		}
	}
	outDir, err := tsconfigOutDir(filepath.Join(source, filepath.FromSlash(config)), nil)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(source, nestDefaultOutDir)
	if outDir != "" {
		out = outDir
	}
	rel, err := filepath.Rel(source, out)
	if err != nil {
		return nil, err
	}
	return []string{filepath.Join(rel, entry), filepath.Join(rel, filepath.FromSlash(sourceRoot), entry)}, nil
}

func tsconfigOutDir(path string, seen []string) (string, error) {
	if slices.Contains(seen, path) {
		return "", fmt.Errorf("%s extends itself", path)
	}
	var config tsconfig
	found, err := readJSONC(path, &config)
	if err != nil || !found {
		return "", err
	}
	if config.CompilerOptions.OutDir != "" {
		return filepath.Join(filepath.Dir(path), filepath.FromSlash(config.CompilerOptions.OutDir)), nil
	}
	var bases []string
	if len(config.Extends) > 0 && json.Unmarshal(config.Extends, &bases) != nil {
		var base string
		if err := json.Unmarshal(config.Extends, &base); err != nil {
			return "", fmt.Errorf("%s: extends is neither a path nor a list of them", path)
		}
		bases = []string{base}
	}
	for _, base := range slices.Backward(bases) {
		if !strings.HasPrefix(base, ".") {
			continue
		}
		outDir, err := tsconfigOutDir(filepath.Join(filepath.Dir(path), filepath.FromSlash(base)), append(seen, path))
		if err != nil || outDir != "" {
			return outDir, err
		}
	}
	return "", nil
}

func readJSONC(path string, into any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	standard, err := hujson.Standardize(data)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(standard, into); err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return true, nil
}

func slashed(paths []string) []string {
	out := make([]string, len(paths))
	for i, path := range paths {
		out[i] = filepath.ToSlash(path)
	}
	return out
}
