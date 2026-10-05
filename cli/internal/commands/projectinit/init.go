package projectinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/docsurl"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
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
	settings   []providerSetting
}

type Dependencies struct {
	commands.Invocation
	RunPackageManager func(ctx context.Context, dir string, argv []string, output io.Writer) error
}

func NewCommand(dependencies Dependencies) *cobra.Command {
	var flags initOptions
	cmd := &cobra.Command{
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

			opts := flags
			opts.configPath = dependencies.ConfigPath()

			return runInitCommand(cmd.Context(), dependencies, cwd, slug, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&flags.provider, "provider", "", "Provider this project deploys through")
	cmd.Flags().StringVar(&flags.language, "lang", "", "Language of this project ("+strings.Join(languageNames(), ", ")+"), when the manifests do not say")
	cmd.Flags().BoolVar(&flags.ts, "ts", false, "Write ocel.config.ts instead of ocel.json — it compiles to the same document and needs node")
	cmd.Flags().BoolVar(&flags.yaml, "yaml", false, "Write ocel.yaml instead of ocel.json — the same document, written as YAML")
	cmd.MarkFlagsMutuallyExclusive("ts", "yaml")
	return commands.DeclareMutating(commands.ReserveStdout(cmd))
}

func runInitCommand(ctx context.Context, dependencies Dependencies, cwd, slug string, opts initOptions, stdout io.Writer) error {
	result, err := runInit(ctx, dependencies, cwd, slug, opts)
	if err != nil {
		return err
	}
	if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, result)
	}
	return nil
}

func runInit(ctx context.Context, dependencies Dependencies, cwd, slug string, opts initOptions) (*resultv1.InitResult, error) {
	configPath, err := initConfigPath(cwd, opts)
	if err != nil {
		return nil, err
	}
	projectDir := filepath.Dir(configPath)
	name := filepath.Base(configPath)

	slug, err = resolveSlug(projectDir, slug)
	if err != nil {
		return nil, err
	}

	provider := strings.TrimSpace(opts.provider)
	shipped := configdoc.ProviderIDs()
	if provider == "" {
		return nil, clierror.NewInputRequired(
			fmt.Errorf("name the provider this project deploys through, e.g. `ocel init --provider <id>` — ocel ships %s", strings.Join(shipped, ", ")),
			"--provider <id>",
		)
	}
	if !slices.Contains(shipped, provider) {
		return nil, fmt.Errorf("--provider names %q, and ocel ships no such provider — name one of %s", provider, strings.Join(shipped, ", "))
	}

	lang, detected, err := languageOfProject(projectDir, opts)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(configPath); err == nil {
		return nil, &clierror.Error{Code: clierror.CodeInitConfigExists, Cause: fmt.Errorf("%s already exists", name)}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("check for existing %s: %w", name, err)
	}
	if others := project.OtherConfigFiles(configPath); len(others) > 0 {
		return nil, &clierror.Error{Code: clierror.CodeInitConfigExists, Cause: fmt.Errorf("%s already contains %s, and one project reads one config: keep it, or delete it before writing %s", projectDir, strings.Join(others, " and "), name)}
	}

	ctx, initializing, err := dependencies.Events.Begin(ctx, "ocel init", "")
	if err != nil {
		return nil, err
	}
	added, err := writeProject(ctx, dependencies, initializing.Phase(progressv1.Phase_PHASE_BUILD), configPath, slug, provider, opts.settings, lang, detected)
	if err == nil {
		initializing.Succeed("Initialized project " + slug)
	}
	initializing.End(&err)
	if err != nil {
		return nil, err
	}
	result := &resultv1.InitResult{
		ConfigPath: configPath,
		Format:     configFormat(name),
		Slug:       slug,
		Provider:   provider,
		Language:   lang.name,
	}
	if added != "" {
		result.SdkPackage = &added
	}
	return result, nil
}

func configFormat(name string) resultv1.ConfigFormat {
	switch {
	case project.IsTypeScript(name):
		return resultv1.ConfigFormat_CONFIG_FORMAT_TYPESCRIPT
	case project.IsYAML(name):
		return resultv1.ConfigFormat_CONFIG_FORMAT_YAML
	}
	return resultv1.ConfigFormat_CONFIG_FORMAT_JSON
}

func writeProject(ctx context.Context, dependencies Dependencies, build *run.Span, configPath, slug, provider string, settings []providerSetting, lang sdkLanguage, detected bool) (added string, err error) {
	projectDir, name := filepath.Dir(configPath), filepath.Base(configPath)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return "", fmt.Errorf("create directory for %s: %w", name, err)
	}
	if err := os.WriteFile(configPath, []byte(configTemplate(name, slug, provider, settings)), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", name, err)
	}
	build.Say(fmt.Sprintf("Wrote %s for project %s", name, slug))

	if detected {
		added = addSDK(ctx, dependencies, build, projectDir, lang)
	} else {
		build.Warn(fmt.Sprintf("No %s here — add the ocel SDK once this directory contains one.", strings.Join(language.ManifestNames(), ", ")))
	}
	build.Say("Run `ocel deploy` to deploy to your own infrastructure, or `ocel dev` to develop against the Ocel console.")
	return added, nil
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

type providerSetting struct {
	name  string
	value string
}

func settingFields(settings []providerSetting, layout string) []string {
	fields := make([]string, 0, len(settings))
	for _, setting := range settings {
		fields = append(fields, fmt.Sprintf(layout, setting.name, setting.value))
	}
	return fields
}

func configTemplate(name, slug, provider string, settings []providerSetting) string {
	if project.IsTypeScript(name) {
		return typescriptTemplate(slug, provider, settings)
	}
	if project.IsYAML(name) {
		return yamlTemplate(slug, provider, settings)
	}
	selected := fmt.Sprintf("{ %q: {} }", provider)
	switch {
	case len(settings) > 0:
		selected = fmt.Sprintf("{ %q: { %s } }", provider, strings.Join(settingFields(settings, "%q: %q"), ", "))
	case configdoc.ProviderNamedAlone(provider):
		selected = strconv.Quote(provider)
	}
	return fmt.Sprintf(`{
  "$schema": %q,
  "slug": %q,
  "provider": %s
}
`, docsurl.FormatSchema(version.Version), slug, selected)
}

func yamlTemplate(slug, provider string, settings []providerSetting) string {
	selected := fmt.Sprintf("\n  %s: {}", provider)
	switch {
	case len(settings) > 0:
		selected = fmt.Sprintf("\n  %s:\n    %s", provider, strings.Join(settingFields(settings, "%s: %q"), "\n    "))
	case configdoc.ProviderNamedAlone(provider):
		selected = " " + provider
	}
	return fmt.Sprintf(`# yaml-language-server: $schema=%s
slug: %q
provider:%s
`, docsurl.FormatSchema(version.Version), slug, selected)
}

func typescriptTemplate(slug, provider string, settings []providerSetting) string {
	options := "{}"
	if len(settings) > 0 {
		options = "{ " + strings.Join(settingFields(settings, "%q: %q"), ", ") + " }"
	}
	return fmt.Sprintf(`import { defineConfig } from "ocel/config";
import %s from "ocel/providers/%s";

export default defineConfig({
  slug: %q,
  provider: %s(%s),
});
`, providerIdentifier(provider), provider, slug, providerIdentifier(provider), options)
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

func addCommand(dir string, lang sdkLanguage) (argv []string, pkg string) {
	if len(lang.addCommand) > 0 {
		return append(slices.Clone(lang.addCommand), lang.sdk), lang.sdk
	}
	pm := detectPackageManager(dir)
	return []string{pm.name, pm.addCommand, sdkPackage}, sdkPackage
}

func addSDK(ctx context.Context, dependencies Dependencies, build *run.Span, dir string, lang sdkLanguage) string {
	argv, pkg := addCommand(dir, lang)
	command := strings.Join(argv, " ")

	span := build.Child(sdkPackage, progress.Adding.Title(fmt.Sprintf("the SDK to this project with `%s`", command)))
	err := dependencies.RunPackageManager(ctx, dir, argv, span.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_STDERR))
	if err != nil {
		span.Warn(fmt.Sprintf("Could not add %s — run `%s` yourself.", sdkPackage, command))
	}
	span.End(err)
	if err != nil {
		return ""
	}
	return pkg
}

func RunPackageManager(ctx context.Context, dir string, argv []string, output io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout = output
	cmd.Stderr = output
	return cmd.Run()
}
