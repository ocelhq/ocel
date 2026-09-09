package projectconfig

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/pkg/configdoc"
)

const (
	DefaultFileName = "ocel.json"
	TSFileName      = "ocel.config.ts"
)

const configStem = "ocel"

const scratchDirName = ".ocel"

const initHint = "run `ocel init` to create one"

type form struct {
	suffix string
	read   func(ctx context.Context, path string) ([]byte, error)
}

func forms() []form {
	return []form{
		{suffix: ".json", read: readJSON},
		{suffix: ".config.ts", read: readTS},
	}
}

func formOf(base string) (string, form, bool) {
	for _, f := range forms() {
		if !strings.HasPrefix(base, configStem) || !strings.HasSuffix(base, f.suffix) {
			continue
		}
		middle := base[len(configStem) : len(base)-len(f.suffix)]
		if middle == "" {
			return "", f, true
		}
		target, found := strings.CutPrefix(middle, ".")
		if !found || target == "" || strings.Contains(target, ".") {
			continue
		}
		return target, f, true
	}
	return "", form{}, false
}

func fileName(target string, f form) string {
	if target == "" {
		return configStem + f.suffix
	}
	return configStem + "." + target + f.suffix
}

func counterpart(base string) string {
	target, mine, ok := formOf(base)
	if !ok {
		return ""
	}
	for _, other := range forms() {
		if other.suffix != mine.suffix {
			return fileName(target, other)
		}
	}
	return ""
}

func Resolve(ctx context.Context, startDir, explicitPath string) (*Config, error) {
	return resolve(ctx, startDir, explicitPath, false)
}

func ResolveOptional(ctx context.Context, startDir, explicitPath string) (*Config, error) {
	return resolve(ctx, startDir, explicitPath, true)
}

func resolve(ctx context.Context, startDir, explicitPath string, optional bool) (*Config, error) {
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
			return &Config{Dir: root, Path: filepath.Join(root, DefaultFileName)}, nil
		}
		return nil, fmt.Errorf("no %s or %s found in %s or any parent directory — %s", DefaultFileName, TSFileName, startDir, initHint)
	}
	return load(ctx, configPath)
}

func defaultConfigFile(dir string) string {
	for _, f := range forms() {
		path := filepath.Join(dir, fileName("", f))
		if isFile(path) {
			return path
		}
	}
	return ""
}

func explicitConfigFile(startDir, explicitPath string) (string, error) {
	path := explicitPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(startDir, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if isDir(abs) {
		return "", fmt.Errorf("config file %s (from --config / OCEL_CONFIG) is a directory, not a config file", abs)
	}
	if !isFile(abs) {
		return "", fmt.Errorf("config file %s (from --config / OCEL_CONFIG) not found", abs)
	}
	return abs, nil
}

func load(ctx context.Context, configPath string) (*Config, error) {
	base := filepath.Base(configPath)
	_, f, ok := formOf(base)
	if !ok {
		return nil, fmt.Errorf("%s is not a config this reads — a config is named %s, %s, or %s and %s with a target between the stem and the suffix", base, DefaultFileName, TSFileName, "ocel.<target>.json", "ocel.<target>.config.ts")
	}
	dir := filepath.Dir(configPath)
	if other := counterpart(base); other != "" && isFile(filepath.Join(dir, other)) {
		return nil, fmt.Errorf("%s holds both %s and %s, and one project reads one config: delete whichever is not the one you author", dir, base, other)
	}

	data, err := f.read(ctx, configPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}

	lookup, err := envLookup(dir)
	if err != nil {
		return nil, err
	}
	doc, err := configdoc.Decode(data, lookup)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configPath, err)
	}
	return normalize(doc, configPath)
}

func readJSON(_ context.Context, configPath string) ([]byte, error) {
	read, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	standard, err := hujson.Standardize(read)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", configPath, err)
	}
	return standard, nil
}

func envLookup(dir string) (configdoc.Lookup, error) {
	file, err := dotenv.Load(dir)
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

func findProjectRoot(startDir string) (string, error) {
	start, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	for dir := start; ; {
		if defaultConfigFile(dir) != "" {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start, nil
		}
		dir = parent
	}
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
