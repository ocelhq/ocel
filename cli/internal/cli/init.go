package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

const sdkPackage = "ocel"

const goSDKModule = "github.com/ocelhq/ocel/sdk"

type initOptions struct {
	provider   string
	language   string
	ts         bool
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
		"Run `ocel console link` to associate this directory with an Ocel console project.",
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

		return runInit(cmd.Context(), newDeps(), cwd, slug, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	initCmd.Flags().StringVar(&initOpts.provider, "provider", "", "Provider this project deploys through")
	initCmd.Flags().StringVar(&initOpts.language, "lang", "", "Language of this project ("+strings.Join(languageNames(), ", ")+"), when the manifests do not say")
	initCmd.Flags().BoolVar(&initOpts.ts, "ts", false, "Write ocel.config.ts instead of ocel.json — it compiles to the same document and needs node")
}

type language struct {
	name     string
	manifest string
	add      []string
}

var languages = []language{
	{name: "go", manifest: "go.mod", add: []string{"go", "get", goSDKModule}},
	{name: "rust", manifest: "Cargo.toml", add: []string{"cargo", "add", sdkPackage}},
	{name: "python", manifest: "pyproject.toml", add: []string{"uv", "add", sdkPackage}},
	{name: "node", manifest: "package.json"},
}

func languageNames() []string {
	names := make([]string, 0, len(languages))
	for _, l := range languages {
		names = append(names, l.name)
	}
	return names
}

func languageNamed(name string) (language, error) {
	for _, l := range languages {
		if l.name == name {
			return l, nil
		}
	}
	return language{}, fmt.Errorf("%q is no language ocel ships an SDK for — name one of %s", name, strings.Join(languageNames(), ", "))
}

func detectLanguage(dir string) (language, bool, error) {
	var found []language
	for _, l := range languages {
		if _, err := os.Stat(filepath.Join(dir, l.manifest)); err == nil {
			found = append(found, l)
		}
	}
	switch len(found) {
	case 0:
		return language{}, false, nil
	case 1:
		return found[0], true, nil
	default:
		names := make([]string, 0, len(found))
		manifests := make([]string, 0, len(found))
		for _, l := range found {
			names = append(names, l.name)
			manifests = append(manifests, l.manifest)
		}
		return language{}, false, fmt.Errorf(
			"this directory holds %s, so it could be a %s project: name the one this is with `--lang %s`",
			strings.Join(manifests, " and "), strings.Join(names, " or "), names[0],
		)
	}
}

func runInit(ctx context.Context, deps cmddeps.Deps, cwd, slug string, opts initOptions, stdout, stderr io.Writer) error {
	configPath := filepath.Join(cwd, configFileName(opts))
	if opts.configPath != "" {
		configPath = opts.configPath
		if !filepath.IsAbs(configPath) {
			configPath = filepath.Join(cwd, configPath)
		}
	}
	projectDir := filepath.Dir(configPath)
	name := filepath.Base(configPath)

	slug, err := resolveSlug(projectDir, slug)
	if err != nil {
		return err
	}

	provider := strings.TrimSpace(opts.provider)
	if provider == "" {
		return errors.New("name the provider this project deploys through, e.g. `ocel init --provider <name>` — the providers ocel ships are listed at https://ocel.dev/docs/providers")
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

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", name, err)
	}
	if err := os.WriteFile(configPath, []byte(configTemplate(name, slug, provider)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	fmt.Fprintf(stdout, "✓ Wrote %s (slug: %s)\n", name, slug)

	if detected {
		addSDK(ctx, deps, projectDir, lang, stdout, stderr)
	} else {
		fmt.Fprintf(stdout, "! No %s here — add the ocel SDK once this directory holds one.\n", strings.Join(manifestNames(), ", "))
	}

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Run `ocel deploy` to deploy to your own infrastructure, or `ocel dev` to develop against the Ocel console.")

	return nil
}

func manifestNames() []string {
	names := make([]string, 0, len(languages))
	for _, l := range languages {
		names = append(names, l.manifest)
	}
	return names
}

func languageOfProject(projectDir string, opts initOptions) (language, bool, error) {
	if opts.language != "" {
		lang, err := languageNamed(opts.language)
		return lang, err == nil, err
	}
	return detectLanguage(projectDir)
}

func configFileName(opts initOptions) string {
	if opts.ts {
		return projectconfig.TSFileName
	}
	return projectconfig.DefaultFileName
}

func resolveSlug(projectDir, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		dir := filepath.Base(projectDir)
		derived := slugify(dir)
		if derived == "" {
			return "", fmt.Errorf("could not derive a slug from directory %q — pass one, e.g. `ocel init my-app`", dir)
		}
		return derived, nil
	}

	if err := projectconfig.ValidateSlug(requested); err != nil {
		return "", fmt.Errorf("invalid slug %w", err)
	}
	return requested, nil
}

func schemaURL() string {
	return "https://ocel.dev/schema/" + version + "/ocel.schema.json"
}

func configTemplate(name, slug, provider string) string {
	if strings.HasSuffix(name, ".ts") {
		return typescriptTemplate(slug, provider)
	}
	return fmt.Sprintf(`{
  "$schema": %q,
  "slug": %q,
  "provider": { "name": %q, "options": {} }
}
`, schemaURL(), slug, provider)
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
	name := slugify(provider)
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

func addCommand(dir string, lang language) []string {
	if len(lang.add) > 0 {
		return slices.Clone(lang.add)
	}
	pm := detectPackageManager(dir)
	return []string{pm.name, pm.addCommand, sdkPackage}
}

func addSDK(ctx context.Context, deps cmddeps.Deps, dir string, lang language, stdout, stderr io.Writer) {
	argv := addCommand(dir, lang)
	command := strings.Join(argv, " ")

	err := withSpinner(stdout, fmt.Sprintf("Adding %s...", sdkPackage), func() error {
		return deps.RunPackageManager(ctx, dir, argv, stderr)
	})
	if err != nil {
		fmt.Fprintf(stdout, "! Could not add %s (%v) — run `%s` yourself.\n", sdkPackage, err, command)
		return
	}
	fmt.Fprintf(stdout, "✓ Added %s\n", sdkPackage)
}
