package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const sdkPackage = "ocel"

const goSDKModule = "ocel.dev"

const rustSDKCrate = "ocel-sdk"

type initOptions struct {
	provider   string
	language   string
	ts         bool
	yaml       bool
	configPath string
}

var initOpts initOptions

var initCmd = &cobra.Command{
	Use:   "init [slug]",
	Short: "Make this directory deployable",
	Long: "Writes ocel.json and adds the ocel SDK with your language's own package manager.\n\n" +
		"Runs entirely offline: it neither signs you in nor contacts the Ocel console.\n\n" +
		"The slug is the project's deployment identity — every stack and resource\n" +
		"ocel creates in your own provider account is keyed on it, so changing it later\n" +
		"forks a new project. It defaults to this directory's name.\n\n" +
		"Run `ocel link` to associate this directory with an Ocel console project.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		slug := ""
		if len(args) > 0 {
			slug = args[0]
		}

		opts := initOpts
		opts.configPath = explicitConfigPath()

		return runInit(cmd.Context(), newDeps(), cwd, slug, opts)
	},
}

func init() {
	initCmd.Flags().StringVar(&initOpts.provider, "provider", "", "Provider this project deploys through")
	initCmd.Flags().StringVar(&initOpts.language, "lang", "", "Language of this project ("+strings.Join(languageNames(), ", ")+"), when the manifests do not say")
	initCmd.Flags().BoolVar(&initOpts.ts, "ts", false, "Write ocel.config.ts instead of ocel.json — it compiles to the same document and needs node")
	initCmd.Flags().BoolVar(&initOpts.yaml, "yaml", false, "Write ocel.yaml instead of ocel.json — the same document, written as YAML")
	initCmd.MarkFlagsMutuallyExclusive("ts", "yaml")
}

type sdkLanguage struct {
	name     string
	language language.Language
	add      []string
}

var sdkLanguages = []sdkLanguage{
	{name: "go", language: language.Go, add: []string{"go", "get", goSDKModule}},
	{name: "rust", language: language.Rust, add: []string{"cargo", "add", rustSDKCrate}},
	{name: "python", language: language.Python, add: []string{"uv", "add", sdkPackage}},
	{name: "node", language: language.JS},
}

func languageNames() []string {
	names := make([]string, 0, len(sdkLanguages))
	for _, l := range sdkLanguages {
		names = append(names, l.name)
	}
	return names
}

func languageNamed(name string) (sdkLanguage, error) {
	for _, l := range sdkLanguages {
		if l.name == name {
			return l, nil
		}
	}
	return sdkLanguage{}, fmt.Errorf("%q is no language ocel ships an SDK for — name one of %s", name, strings.Join(languageNames(), ", "))
}

func sdkLanguageOf(written language.Language) sdkLanguage {
	for _, l := range sdkLanguages {
		if l.language == written {
			return l
		}
	}
	return sdkLanguage{}
}

func detectLanguage(dir string) (sdkLanguage, bool, error) {
	found := language.Manifested(dir)
	switch len(found) {
	case 0:
		return sdkLanguage{}, false, nil
	case 1:
		return sdkLanguageOf(found[0]), true, nil
	default:
		names := make([]string, 0, len(found))
		for _, l := range found {
			names = append(names, sdkLanguageOf(l).name)
		}
		return sdkLanguage{}, false, fmt.Errorf(
			"this directory contains the manifests of %s at once, so it could be a %s project: name the one this is with `--lang %s`",
			strings.Join(names, " and "), strings.Join(names, " or "), names[0],
		)
	}
}

func runInit(ctx context.Context, deps cmddeps.Deps, cwd, slug string, opts initOptions) error {
	configPath, err := initConfigPath(cwd, opts)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(configPath)
	name := filepath.Base(configPath)

	slug, err = resolveSlug(projectDir, slug)
	if err != nil {
		return err
	}

	provider := strings.TrimSpace(opts.provider)
	shipped := configdoc.ProviderIDs()
	if provider == "" {
		return fmt.Errorf("name the provider this project deploys through, e.g. `ocel init --provider <id>` — ocel ships %s", strings.Join(shipped, ", "))
	}
	if !slices.Contains(shipped, provider) {
		return fmt.Errorf("--provider names %q, and ocel ships no such provider — name one of %s", provider, strings.Join(shipped, ", "))
	}

	lang, detected, err := languageOfProject(projectDir, opts)
	if err != nil {
		return err
	}

	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("%s already exists", name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check for existing %s: %w", name, err)
	}
	if others := project.OtherConfigFiles(configPath); len(others) > 0 {
		return fmt.Errorf("%s already contains %s, and one project reads one config: keep it, or delete it before writing %s", projectDir, strings.Join(others, " and "), name)
	}

	ctx, initializing, err := deps.Events.Begin(ctx, "ocel init", "")
	if err != nil {
		return err
	}
	err = writeProject(ctx, deps, initializing.Phase(progressv1.Phase_PHASE_BUILD), configPath, slug, provider, lang, detected)
	if err == nil {
		initializing.Succeed("Initialized project " + slug)
	}
	initializing.End(&err)
	return err
}

func writeProject(ctx context.Context, deps cmddeps.Deps, build *run.Span, configPath, slug, provider string, lang sdkLanguage, detected bool) error {
	projectDir, name := filepath.Dir(configPath), filepath.Base(configPath)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", name, err)
	}
	if err := os.WriteFile(configPath, []byte(configTemplate(name, slug, provider)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	build.Say(fmt.Sprintf("Wrote %s for project %s", name, slug))

	if detected {
		addSDK(ctx, deps, build, projectDir, lang)
	} else {
		build.Warn(fmt.Sprintf("No %s here — add the ocel SDK once this directory contains one.", strings.Join(language.ManifestNames(), ", ")))
	}
	build.Say("Run `ocel deploy` to deploy to your own infrastructure, or `ocel dev` to develop against the Ocel console.")
	return nil
}

func languageOfProject(projectDir string, opts initOptions) (sdkLanguage, bool, error) {
	if opts.language != "" {
		lang, err := languageNamed(opts.language)
		return lang, err == nil, err
	}
	return detectLanguage(projectDir)
}

func initConfigPath(cwd string, opts initOptions) (string, error) {
	if opts.configPath == "" {
		return filepath.Join(cwd, configFileName(opts)), nil
	}
	path := opts.configPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	name := filepath.Base(path)
	switch {
	case !project.IsConfig(name):
		return "", fmt.Errorf("%s (from --config / OCEL_CONFIG) is not a config ocel reads — name it %s, %s or %s, with an optional target before the suffix", name, project.DefaultFileName, project.YAMLFileName, project.TSFileName)
	case opts.ts && !project.IsTypeScript(name):
		return "", fmt.Errorf("--ts writes a TypeScript config, and %s (from --config / OCEL_CONFIG) is not one: name it %s, or drop --ts", name, project.TSFileName)
	case opts.yaml && !project.IsYAML(name):
		return "", fmt.Errorf("--yaml writes a YAML config, and %s (from --config / OCEL_CONFIG) is not one: name it %s, or drop --yaml", name, project.YAMLFileName)
	}
	return path, nil
}

func configFileName(opts initOptions) string {
	switch {
	case opts.ts:
		return project.TSFileName
	case opts.yaml:
		return project.YAMLFileName
	}
	return project.DefaultFileName
}

func resolveSlug(projectDir, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		dir := filepath.Base(projectDir)
		derived := project.DeriveSlug(dir)
		if derived == "" {
			return "", fmt.Errorf("could not derive a slug from directory %q — pass one, e.g. `ocel init my-app`", dir)
		}
		return derived, nil
	}

	if err := project.ValidateSlug(requested); err != nil {
		return "", fmt.Errorf("invalid slug %w", err)
	}
	return requested, nil
}

func schemaURL() string {
	return "https://ocel.dev/schema/" + version.Version + "/ocel.schema.json"
}

func configTemplate(name, slug, provider string) string {
	if project.IsTypeScript(name) {
		return typescriptTemplate(slug, provider)
	}
	if project.IsYAML(name) {
		return yamlTemplate(slug, provider)
	}
	selected := fmt.Sprintf("{ %q: {} }", provider)
	if configdoc.ProviderNamedAlone(provider) {
		selected = strconv.Quote(provider)
	}
	return fmt.Sprintf(`{
  "$schema": %q,
  "slug": %q,
  "provider": %s
}
`, schemaURL(), slug, selected)
}

func yamlTemplate(slug, provider string) string {
	selected := fmt.Sprintf("\n  %s: {}", provider)
	if configdoc.ProviderNamedAlone(provider) {
		selected = " " + provider
	}
	return fmt.Sprintf(`# yaml-language-server: $schema=%s
slug: %q
provider:%s
`, schemaURL(), slug, selected)
}

func typescriptTemplate(slug, provider string) string {
	return fmt.Sprintf(`import { defineConfig } from "ocel/config";
import %s from "ocel/providers/%s";

export default defineConfig({
  slug: %q,
  provider: %s({}),
});
`, providerIdentifier(provider), provider, slug, providerIdentifier(provider))
}

func providerIdentifier(provider string) string {
	name := project.DeriveSlug(provider)
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		return "provider"
	}

	parts := strings.Split(name, "-")
	ident := parts[0]
	for _, part := range parts[1:] {
		ident += strings.ToUpper(part[:1]) + part[1:]
	}
	return ident + "Provider"
}

type packageManager struct {
	name       string
	lockfile   string
	addCommand string
}

var npmPackageManager = packageManager{name: "npm", lockfile: "package-lock.json", addCommand: "install"}

var packageManagers = []packageManager{
	{name: "pnpm", lockfile: "pnpm-lock.yaml", addCommand: "add"},
	{name: "yarn", lockfile: "yarn.lock", addCommand: "add"},
	{name: "bun", lockfile: "bun.lockb", addCommand: "add"},
	npmPackageManager,
}

func detectPackageManager(dir string) packageManager {
	for _, pm := range packageManagers {
		if _, err := os.Stat(filepath.Join(dir, pm.lockfile)); err == nil {
			return pm
		}
	}
	return npmPackageManager
}

func addCommand(dir string, lang sdkLanguage) []string {
	if len(lang.add) > 0 {
		return slices.Clone(lang.add)
	}
	pm := detectPackageManager(dir)
	return []string{pm.name, pm.addCommand, sdkPackage}
}

func addSDK(ctx context.Context, deps cmddeps.Deps, build *run.Span, dir string, lang sdkLanguage) {
	argv := addCommand(dir, lang)
	command := strings.Join(argv, " ")

	unit := build.Unit(sdkPackage, progress.Adding.Title(fmt.Sprintf("the SDK to this project with `%s`", command)))
	err := deps.RunPackageManager(ctx, dir, argv, unit.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDERR))
	if err != nil {
		unit.Warn(fmt.Sprintf("Could not add %s — run `%s` yourself.", sdkPackage, command))
	}
	unit.End(err)
}
