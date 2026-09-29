package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/dotfile"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/envsource"
)

func Resolve(ctx context.Context, startDir, explicitPath string) (*Project, error) {
	return resolve(ctx, startDir, explicitPath, false)
}

func ResolveOptional(ctx context.Context, startDir, explicitPath string) (*Project, error) {
	return resolve(ctx, startDir, explicitPath, true)
}

func resolve(ctx context.Context, startDir, explicitPath string, optional bool) (*Project, error) {
	if explicitPath != "" {
		configPath, err := explicitConfigFile(startDir, explicitPath)
		if err != nil {
			return nil, err
		}
		return load(ctx, configPath)
	}

	root, err := findProjectRoot(startDir)
	if err != nil {
		return nil, err
	}

	configPath := defaultConfigFile(root)
	if configPath == "" {
		if optional {
			return &Project{Dir: root, Path: filepath.Join(root, DefaultFileName), EnvSource: envsource.DefaultTiers()}, nil
		}
		return nil, NoConfigError{Names: fileNames(""), StartDir: startDir}
	}
	return load(ctx, configPath)
}

func load(ctx context.Context, configPath string) (*Project, error) {
	base := filepath.Base(configPath)
	_, f, ok := formOf(base)
	if !ok {
		return nil, fmt.Errorf("%s is not a config this reads — a config is named %s, or %s with a target between the stem and the suffix", base, strings.Join(fileNames(""), ", "), strings.Join(fileNames("<target>"), ", "))
	}
	dir := filepath.Dir(configPath)
	if others := OtherConfigFiles(configPath); len(others) > 0 {
		return nil, fmt.Errorf("%s contains %s, and one project reads one config: delete all but the one you author", dir, strings.Join(append([]string{base}, others...), " and "))
	}

	data, err := f.read(ctx, configPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	lookup, err := EnvLookup(dir)
	if err != nil {
		return nil, err
	}
	doc, err := configdoc.Decode(data, lookup)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}
	return normalize(doc, configPath)
}

func EnvLookup(dir string) (configdoc.Lookup, error) {
	file, err := dotfile.Load(dir)
	if err != nil {
		return nil, err
	}
	return func(name string) (string, bool) {
		if value, set := os.LookupEnv(name); set {
			return value, true
		}
		value, set := file.Values[name]
		return value, set
	}, nil
}
