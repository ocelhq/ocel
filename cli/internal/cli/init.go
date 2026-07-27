package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/authclient"
	"github.com/ocelhq/ocel/cli/internal/projectclient"
)

// initOptions holds the flags accepted by `ocel init`.
type initOptions struct {
	yes    bool
	org    string
	apiURL string
}

var initOpts initOptions

// initCmd scaffolds a new Ocel project.
var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Create a new Ocel project",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		name := ""
		if len(args) > 0 {
			name = args[0]
		}

		opts := initOpts
		// An explicit --api-url wins; otherwise fall back to the persisted
		// credentials' API URL, then the resolved default (effectiveAPIURL).
		creds, _ := loadCredentials()
		opts.apiURL = effectiveAPIURL(cmd, creds.APIURL)

		return runInit(cmd.Context(), cwd, name, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func init() {
	initCmd.Flags().BoolVarP(&initOpts.yes, "yes", "y", false, "Skip the confirmation prompt")
	initCmd.Flags().StringVar(&initOpts.org, "org", "", "Select an organization by slug, bypassing the interactive picker")
}

// runInit resolves a project name, confirms with the user, resolves their
// organization, creates the project via the control-plane API, and
// scaffolds ocel.config.ts in cwd.
func runInit(ctx context.Context, cwd string, name string, opts initOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	scanner := bufio.NewScanner(stdin)

	name = strings.TrimSpace(name)
	if name == "" {
		if !isReaderTTY(stdin) {
			return errors.New("project name required — pass it as an argument, e.g. `ocel init my-app`")
		}
		fmt.Fprint(stdout, "Project name: ")
		if scanner.Scan() {
			name = strings.TrimSpace(scanner.Text())
		}
		if name == "" {
			return errors.New("project name required — pass it as an argument, e.g. `ocel init my-app`")
		}
	}

	slug := slugify(name)
	if slug == "" {
		return fmt.Errorf("could not derive a valid slug from %q — try a name with at least one alphanumeric character", name)
	}

	configPath := filepath.Join(cwd, "ocel.config.ts")
	if _, err := os.Stat(configPath); err == nil {
		return errors.New("ocel.config.ts already exists in this directory.")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check for existing ocel.config.ts: %w", err)
	}

	creds, err := loadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &ExitError{Code: 1}
	}

	fmt.Fprintf(stdout, "This will create project %q in the current directory.\n", name)
	if !opts.yes {
		fmt.Fprint(stdout, "Continue? (Y/n) ")
		// No line available to read (e.g. a closed/empty pipe) means there's
		// no one there to answer the prompt; a real TTY simply blocks here
		// until the user responds, and a piped answer (like "n\n" in tests)
		// is read and honored either way.

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("failed to read input: %w", err)
			}
			return errors.New("confirmation required — pass --yes to run non-interactively.")
		}

		answer := strings.TrimSpace(scanner.Text())
		if answer == "n" || strings.EqualFold(answer, "no") {
			fmt.Fprintln(stdout, "Aborted.")
			return nil
		}
	}

	apiURL := strings.TrimRight(opts.apiURL, "/")
	authClient := authclient.New(apiURL)
	projectClient := projectclient.New(apiURL)

	org, err := resolveOrganization(ctx, authClient, creds.AccessToken, opts, stdout, stdin, scanner)
	if err != nil {
		return err
	}

	if err := authClient.SetActiveOrganization(ctx, creds.AccessToken, org.ID); err != nil {
		return fmt.Errorf("failed to set active organization: %w", err)
	}
	fmt.Fprintf(stdout, "✓ Using organization %s\n", org.Name)

	var project *projectclient.Project
	err = withSpinner(stdout, fmt.Sprintf("Creating project %q...", name), func() error {
		p, createErr := projectClient.CreateProject(ctx, creds.AccessToken, name, slug)
		if createErr != nil {
			return createErr
		}
		project = p
		return nil
	})
	if err != nil {
		if projectclient.IsConflict(err) {
			return fmt.Errorf("a project named %q already exists in %s. Try `ocel init` with a different name.", name, org.Name)
		}
		return fmt.Errorf("failed to create project: %w", err)
	}
	fmt.Fprintf(stdout, "✓ Created project (id: %s)\n", project.ID)

	configSlug := project.Slug
	if configSlug == "" {
		configSlug = slug
	}
	configContent := fmt.Sprintf("import { defineConfig } from \"@ocel/sdk/config\";\n\nexport default defineConfig({\n  slug: %q,\n  projectId: %q,\n});\n", configSlug, project.ID)
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		return fmt.Errorf("created project (id: %s) but failed to write ocel.config.ts: %w", project.ID, err)
	}
	fmt.Fprintln(stdout, "✓ Wrote ocel.config.ts")

	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Run `ocel dev` to start local development.")

	return nil
}

// resolveOrganization determines which organization to use for the new
// project: the sole organization the user belongs to, the one matching
// opts.org, or an interactive pick among several. scanner reads from the
// same stdin as the rest of runInit's prompts — it must be reused rather
// than wrapped again, since a second bufio.Scanner over the same
// underlying reader could silently drop bytes already buffered by the
// first one.
func resolveOrganization(ctx context.Context, client *authclient.Client, accessToken string, opts initOptions, stdout io.Writer, stdin io.Reader, scanner *bufio.Scanner) (*authclient.Organization, error) {
	var orgs []authclient.Organization
	err := withSpinner(stdout, "Resolving organization...", func() error {
		list, listErr := client.ListOrganizations(ctx, accessToken)
		if listErr != nil {
			return listErr
		}
		orgs = list
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to resolve organization: %w", err)
	}

	if len(orgs) == 0 {
		return nil, errors.New("you don't belong to any organization yet — create one on the Ocel dashboard first.")
	}

	if len(orgs) == 1 {
		return &orgs[0], nil
	}

	if opts.org != "" {
		for i := range orgs {
			if orgs[i].Slug == opts.org {
				return &orgs[i], nil
			}
		}
		return nil, fmt.Errorf("no organization with slug %q found; available: %s", opts.org, joinSlugs(orgs))
	}

	nonInteractive := opts.yes || !isReaderTTY(stdin)
	if nonInteractive {
		return nil, fmt.Errorf("multiple organizations found; pass --org <slug>. available: %s", joinSlugs(orgs))
	}

	fmt.Fprintln(stdout, "Multiple organizations found:")
	for i, org := range orgs {
		fmt.Fprintf(stdout, "  %d) %s (%s)\n", i+1, org.Name, org.Slug)
	}
	fmt.Fprint(stdout, "Select an organization (number or slug): ")

	selection := ""
	if scanner.Scan() {
		selection = strings.TrimSpace(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}
	if selection == "" {
		return nil, errors.New("no organization selected; rerun `ocel init`.")
	}

	if idx, convErr := strconv.Atoi(selection); convErr == nil {
		if idx < 1 || idx > len(orgs) {
			return nil, fmt.Errorf("invalid selection %q; rerun `ocel init`.", selection)
		}
		return &orgs[idx-1], nil
	}

	for i := range orgs {
		if orgs[i].Slug == selection {
			return &orgs[i], nil
		}
	}
	return nil, fmt.Errorf("invalid selection %q; rerun `ocel init`.", selection)
}

// joinSlugs comma-joins the slugs of orgs, for use in error messages.
func joinSlugs(orgs []authclient.Organization) string {
	slugs := make([]string, len(orgs))
	for i, org := range orgs {
		slugs[i] = org.Slug
	}
	return strings.Join(slugs, ", ")
}
