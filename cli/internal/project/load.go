package project

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/envsource"
)

func Load(ctx context.Context, startDir, explicitPath string) (*Project, error) {
	return find(ctx, startDir, explicitPath, false)
}

func LoadOptional(ctx context.Context, startDir, explicitPath string) (*Project, error) {
	return find(ctx, startDir, explicitPath, true)
}

func find(ctx context.Context, startDir, explicitPath string, optional bool) (*Project, error) {
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
			apps, err := rootApp(DeriveSlug(filepath.Base(root)), root)
			if err != nil {
				return nil, err
			}
			return &Project{Dir: root, Path: filepath.Join(root, DefaultFileName), EnvSource: envsource.DefaultTiers(), Apps: apps}, nil
		}
		return nil, newNoConfigError(NoConfigError{Names: fileNames(""), StartDir: startDir}, initCommand)
	}
	return load(ctx, configPath)
}

func load(ctx context.Context, configPath string) (*Project, error) {
	base := filepath.Base(configPath)
	_, f, ok := formOf(base)
	if !ok {
		return nil, newNoConfigError(fmt.Errorf("%s is not a config this reads — a config is named %s, or %s with a target between the stem and the suffix", base, strings.Join(fileNames(""), ", "), strings.Join(fileNames("<target>"), ", ")), "")
	}
	dir := filepath.Dir(configPath)
	if others := OtherConfigFiles(configPath); len(others) > 0 {
		return nil, newInvalidConfigError(fmt.Errorf("%s contains %s, and one project reads one config: delete all but the one you author", dir, strings.Join(append([]string{base}, others...), " and ")), "")
	}

	env, err := readEnvironment(dir)
	if err != nil {
		return nil, err
	}
	data, err := f.load(ctx, configPath, env)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	doc, err := configdoc.Decode(data, env.lookup)
	if err != nil {
		var unknown configdoc.UnknownKeyError
		errors.As(err, &unknown)
		return nil, newInvalidConfigError(fmt.Errorf("%s: %w", configPath, err), unknown.Path)
	}
	return normalize(doc, configPath)
}
