package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
)

const (
	DefaultFileName = configStem + jsonSuffix
	YAMLFileName    = configStem + yamlSuffix
	TSFileName      = configStem + tsSuffix
)

const (
	configStem = "ocel"
	jsonSuffix = ".json"
	yamlSuffix = ".yaml"
	ymlSuffix  = ".yml"
	tsSuffix   = ".config.ts"
)

func IsConfig(path string) bool {
	_, _, ok := formOf(filepath.Base(path))
	return ok
}

func IsTypeScript(path string) bool {
	_, f, ok := formOf(filepath.Base(path))
	return ok && f.suffix == tsSuffix
}

func IsYAML(path string) bool {
	_, f, ok := formOf(filepath.Base(path))
	return ok && (f.suffix == yamlSuffix || f.suffix == ymlSuffix)
}

const initCommand = "ocel init"

type NoConfigError struct {
	Names    []string
	StartDir string
}

func (e NoConfigError) Error() string {
	return fmt.Sprintf("no %s found in %s or any parent directory.\nRun `%s` and try again", english.Or(e.Names), e.StartDir, initCommand)
}

func (NoConfigError) Missing() prerequisite.Kind { return prerequisite.Project }

func (e NoConfigError) Finding() string {
	return fmt.Sprintf("No %s in %s or any parent.", english.Or(e.Names), e.StartDir)
}

func (NoConfigError) Remedy() string { return "`" + initCommand + "`" }

type form struct {
	suffix string
	load   func(ctx context.Context, path string, env environment) ([]byte, error)
}

func forms() []form {
	return []form{
		{suffix: jsonSuffix, load: readJSON},
		{suffix: yamlSuffix, load: readYAML},
		{suffix: ymlSuffix, load: readYAML},
		{suffix: tsSuffix, load: evaluateTypeScript},
	}
}

func fileNames(target string) []string {
	names := make([]string, 0, len(forms()))
	for _, f := range forms() {
		names = append(names, fileName(target, f))
	}
	return names
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

func OtherConfigFiles(configPath string) []string {
	dir, base := filepath.Dir(configPath), filepath.Base(configPath)
	target, mine, ok := formOf(base)
	if !ok {
		return nil
	}
	var found []string
	for _, other := range forms() {
		name := fileName(target, other)
		if other.suffix != mine.suffix && isRegularFile(filepath.Join(dir, name)) {
			found = append(found, name)
		}
	}
	return found
}

func defaultConfigFile(dir string) string {
	for _, f := range forms() {
		path := filepath.Join(dir, fileName("", f))
		if isRegularFile(path) {
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
		return "", newNoConfigError(fmt.Errorf("config file %s (from --config / OCEL_CONFIG) is a directory, not a config file", abs), "")
	}
	if !isRegularFile(abs) {
		return "", newNoConfigError(fmt.Errorf("config file %s (from --config / OCEL_CONFIG) not found", abs), "")
	}
	return abs, nil
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

func readJSON(_ context.Context, configPath string, _ environment) ([]byte, error) {
	read, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", configPath, err)
	}
	standard, err := hujson.Standardize(read)
	if err != nil {
		return nil, newInvalidConfigError(fmt.Errorf("%s is not valid JSON: %w", configPath, err), "")
	}
	return standard, nil
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
