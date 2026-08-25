package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/session"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

const sdkPackage = "ocel"

const defaultProviderPackage = "@ocel/provider-aws"

type initOptions struct {
	provider   string
	configPath string
}

var initOpts initOptions

var initCmd = &cobra.Command{
	Use:   "init [slug]",
	Short: "Make this directory deployable",
	Long: "Writes ocel.config.ts and adds ocel and the provider package to your dependencies.\n\n" +
		"Runs entirely offline: it neither signs you in nor contacts the Ocel console.\n\n" +
		"The slug is the project's deployment identity — every stack and resource\n" +
		"ocel creates in your own cloud account is keyed on it, so changing it later\n" +
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

		return runInit(cmd.Context(), newSession(), cwd, slug, opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
	},
}

func init() {
	initCmd.Flags().StringVar(&initOpts.provider, "provider", defaultProviderPackage, "Provider package to scaffold with")
}

func runInit(ctx context.Context, d session.Session, cwd, slug string, opts initOptions, stdout, stderr io.Writer) error {
	configPath := filepath.Join(cwd, projectconfig.ConfigFileName)
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

	providerPkg := strings.TrimSpace(opts.provider)
	if providerPkg == "" {
		providerPkg = defaultProviderPackage
	}

	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("%s already exists", name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check for existing %s: %w", name, err)
	}

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", name, err)
	}
	if err := os.WriteFile(configPath, []byte(configTemplate(slug, providerPkg)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	fmt.Fprintf(stdout, "✓ Wrote %s (slug: %s)\n", name, slug)

	addDependencies(ctx, d, projectDir, []string{sdkPackage, providerPkg}, stdout, stderr)

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Run `ocel deploy` to deploy to your own cloud, or `ocel dev` to develop against the Ocel console.")

	return nil
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

func configTemplate(slug, providerPkg string) string {
	provider := providerIdentifier(providerPkg)
	return fmt.Sprintf(`import { defineConfig } from "ocel/config";
import %s from %q;

export default defineConfig({
  slug: %q,
  provider: %s(),
});
`, provider, providerPkg, slug, provider)
}

func providerIdentifier(pkg string) string {
	base := pkg[strings.LastIndex(pkg, "/")+1:]
	name := slugify(strings.TrimPrefix(strings.ToLower(base), "provider-"))
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

func addDependencies(ctx context.Context, d session.Session, dir string, pkgs []string, stdout, stderr io.Writer) {
	pm := detectPackageManager(dir)
	argv := append([]string{pm.name, pm.addCommand}, pkgs...)
	command := strings.Join(argv, " ")
	added := strings.Join(pkgs, " ")

	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		fmt.Fprintf(stdout, "! No package.json here — run `%s` once you have one.\n", command)
		return
	}

	err := withSpinner(stdout, fmt.Sprintf("Adding %s...", added), func() error {
		return d.RunPackageManager(ctx, dir, argv, stderr)
	})
	if err != nil {
		fmt.Fprintf(stdout, "! Could not add %s (%v) — run `%s` yourself.\n", added, err, command)
		return
	}
	fmt.Fprintf(stdout, "✓ Added %s\n", added)
}
