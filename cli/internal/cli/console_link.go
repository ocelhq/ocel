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
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/consolebinding"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/projectclient"
)

type consoleLinkOptions struct {
	org    string
	create bool
	apiURL string
}

var consoleLinkOpts consoleLinkOptions

var consoleLinkCmd = &cobra.Command{
	Use:   "link [project]",
	Short: "Link this directory to an Ocel console project",
	Long: "Records this working tree's console project in .ocel/console.json,\n" +
		"which is untracked — a clone can be linked to a different account or\n" +
		"project, or to none at all.\n\n" +
		"With no arguments on a terminal, pick from your existing projects or\n" +
		"create a new one. Otherwise name an existing project's slug, or pass\n" +
		"--create to make a new project named after this directory.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}

		project := ""
		if len(args) > 0 {
			project = args[0]
		}

		deps := newDeps()
		opts := consoleLinkOpts
		creds, _ := deps.LoadCredentials()
		opts.apiURL = effectiveAPIURL(creds.APIURL)

		return runConsoleLink(cmd.Context(), deps, cwd, project, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
	},
}

func runConsoleLink(ctx context.Context, deps cmddeps.Deps, projectDir, project string, opts consoleLinkOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	creds, err := deps.LoadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &exitsig.ExitError{Code: 1}
	}

	apiURL := strings.TrimRight(opts.apiURL, "/")
	// TODO: consoleLinkCmd installs no interrupt handler, so SIGINT still hard-kills here —
	// migrating these reads to the prompt package without also adding installInterruptHandler
	// would look like a cleanup but would reintroduce the raw-mode/masked-SIGINT bug
	// this package's other commands fixed (see #245).
	scanner := bufio.NewScanner(stdin)
	project = strings.TrimSpace(project)

	if existing, err := consolebinding.Read(projectDir, apiURL); err != nil {
		return err
	} else if existing != nil {
		fmt.Fprintf(stdout, "This directory is linked to %s. Re-linking.\n", existing.ProjectName)
	}

	authClient := authclient.New(apiURL)
	projectClient := projectclient.New(apiURL)

	org, err := pickOrganization(ctx, authClient, creds.AccessToken, opts, stdout, stdin, scanner)
	if err != nil {
		return err
	}
	if err := authClient.SetActiveOrganization(ctx, creds.AccessToken, org.ID); err != nil {
		return fmt.Errorf("failed to set active organization: %w", err)
	}
	fmt.Fprintf(stdout, "✓ Using organization %s\n", org.Name)

	var projects []projectclient.Project
	err = withSpinner(stdout, "Loading projects...", func() error {
		list, listErr := projectClient.ListProjects(ctx, creds.AccessToken)
		if listErr != nil {
			return listErr
		}
		projects = list
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}

	selected, err := selectOrCreateProject(ctx, projectClient, creds.AccessToken, projectDir, project, opts, projects, org, stdout, stdin, scanner)
	if err != nil {
		return err
	}

	if err := consolebinding.Write(projectDir, consolebinding.Binding{
		APIURL:         apiURL,
		OrganizationID: org.ID,
		ProjectID:      selected.ID,
		ProjectName:    selected.Name,
	}); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "✓ Linked to %s (%s)\n", selected.Name, selected.Slug)
	return nil
}

func ensureConsoleBinding(ctx context.Context, deps cmddeps.Deps, projectDir, apiURL string, stdout, stderr io.Writer, stdin io.Reader) (*consolebinding.Binding, error) {
	binding, err := consolebinding.Read(projectDir, apiURL)
	if err != nil {
		return nil, err
	}
	if binding != nil {
		return binding, nil
	}

	if !isReaderTTY(stdin) {
		return nil, fmt.Errorf("%s isn't linked to a console project — run `ocel console link <project>` (or `ocel console link --create`) first", projectDir)
	}

	fmt.Fprintln(stdout, "This directory isn't linked to a console project yet.")
	if err := runConsoleLink(ctx, deps, projectDir, "", consoleLinkOptions{apiURL: apiURL}, stdout, stderr, stdin); err != nil {
		return nil, err
	}

	binding, err = consolebinding.Read(projectDir, apiURL)
	if err != nil {
		return nil, err
	}
	if binding == nil {
		return nil, errors.New("linking recorded no project — run `ocel console link` and try again")
	}
	return binding, nil
}

func selectOrCreateProject(
	ctx context.Context,
	client *projectclient.Client,
	accessToken, projectDir, project string,
	opts consoleLinkOptions,
	projects []projectclient.Project,
	org *authclient.Organization,
	stdout io.Writer,
	stdin io.Reader,
	scanner *bufio.Scanner,
) (*projectclient.Project, error) {
	if opts.create {
		return createProject(ctx, client, accessToken, defaultProjectName(projectDir, project), org, stdout)
	}

	if project != "" {
		for i := range projects {
			if projects[i].Slug == project {
				return &projects[i], nil
			}
		}
		if len(projects) == 0 {
			return nil, fmt.Errorf("%s has no projects yet — run `ocel console link --create` to make one", org.Name)
		}
		return nil, fmt.Errorf("no project with slug %q in %s; available: %s (or pass --create)", project, org.Name, joinProjectSlugs(projects))
	}

	if !isReaderTTY(stdin) {
		if len(projects) == 0 {
			return nil, errors.New("no project selected — pass --create to make one")
		}
		return nil, fmt.Errorf("no project selected — pass a project slug or --create. available: %s", joinProjectSlugs(projects))
	}

	if len(projects) == 0 {
		fmt.Fprintf(stdout, "%s has no projects yet.\n", org.Name)
		return createProject(ctx, client, accessToken, promptProjectName(projectDir, stdout, scanner), org, stdout)
	}

	fmt.Fprintf(stdout, "Projects in %s:\n", org.Name)
	for i, p := range projects {
		fmt.Fprintf(stdout, "  %d) %s (%s)\n", i+1, p.Name, p.Slug)
	}
	fmt.Fprintln(stdout, "  n) Create a new project")
	fmt.Fprint(stdout, "Select a project (number, slug, or n): ")

	selection := ""
	if scanner.Scan() {
		selection = strings.TrimSpace(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}
	switch {
	case selection == "":
		return nil, errors.New("no project selected; rerun `ocel console link`")
	case strings.EqualFold(selection, "n"):
		return createProject(ctx, client, accessToken, promptProjectName(projectDir, stdout, scanner), org, stdout)
	}

	if idx, convErr := strconv.Atoi(selection); convErr == nil {
		if idx < 1 || idx > len(projects) {
			return nil, fmt.Errorf("invalid selection %q; rerun `ocel console link`", selection)
		}
		return &projects[idx-1], nil
	}
	for i := range projects {
		if projects[i].Slug == selection {
			return &projects[i], nil
		}
	}
	return nil, fmt.Errorf("invalid selection %q; rerun `ocel console link`", selection)
}

func createProject(ctx context.Context, client *projectclient.Client, accessToken, name string, org *authclient.Organization, stdout io.Writer) (*projectclient.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project name required — pass it as an argument, e.g. `ocel console link --create my-app`")
	}
	slug := slugify(name)
	if slug == "" {
		return nil, fmt.Errorf("could not derive a valid slug from %q — try a name with at least one alphanumeric character", name)
	}

	var created *projectclient.Project
	err := withSpinner(stdout, fmt.Sprintf("Creating project %q...", name), func() error {
		p, createErr := client.CreateProject(ctx, accessToken, name, slug)
		if createErr != nil {
			return createErr
		}
		created = p
		return nil
	})
	if err != nil {
		if projectclient.IsConflict(err) {
			return nil, fmt.Errorf("a project with slug %q already exists in %s — run `ocel console link %s` to link to it", slug, org.Name, slug)
		}
		return nil, fmt.Errorf("failed to create project: %w", err)
	}
	fmt.Fprintf(stdout, "✓ Created project (id: %s)\n", created.ID)
	return created, nil
}

func defaultProjectName(projectDir, name string) string {
	if name != "" {
		return name
	}
	return filepath.Base(projectDir)
}

func promptProjectName(projectDir string, stdout io.Writer, scanner *bufio.Scanner) string {
	fallback := filepath.Base(projectDir)
	fmt.Fprintf(stdout, "Project name (%s): ", fallback)
	if scanner.Scan() {
		if name := strings.TrimSpace(scanner.Text()); name != "" {
			return name
		}
	}
	return fallback
}

func pickOrganization(ctx context.Context, client *authclient.Client, accessToken string, opts consoleLinkOptions, stdout io.Writer, stdin io.Reader, scanner *bufio.Scanner) (*authclient.Organization, error) {
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
		return nil, fmt.Errorf("failed to list organizations: %w", err)
	}

	if len(orgs) == 0 {
		return nil, errors.New("you don't belong to any organization yet — create one on the Ocel dashboard first")
	}

	if opts.org != "" {
		for i := range orgs {
			if orgs[i].Slug == opts.org {
				return &orgs[i], nil
			}
		}
		return nil, fmt.Errorf("no organization with slug %q found; available: %s", opts.org, joinOrgSlugs(orgs))
	}

	if len(orgs) == 1 {
		return &orgs[0], nil
	}

	if !isReaderTTY(stdin) {
		return nil, fmt.Errorf("multiple organizations found; pass --org <slug>. available: %s", joinOrgSlugs(orgs))
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
		return nil, errors.New("no organization selected; rerun `ocel console link`")
	}

	if idx, convErr := strconv.Atoi(selection); convErr == nil {
		if idx < 1 || idx > len(orgs) {
			return nil, fmt.Errorf("invalid selection %q; rerun `ocel console link`", selection)
		}
		return &orgs[idx-1], nil
	}
	for i := range orgs {
		if orgs[i].Slug == selection {
			return &orgs[i], nil
		}
	}
	return nil, fmt.Errorf("invalid selection %q; rerun `ocel console link`", selection)
}

func joinOrgSlugs(orgs []authclient.Organization) string {
	slugs := make([]string, len(orgs))
	for i, org := range orgs {
		slugs[i] = org.Slug
	}
	return strings.Join(slugs, ", ")
}

func joinProjectSlugs(projects []projectclient.Project) string {
	slugs := make([]string, len(projects))
	for i, p := range projects {
		slugs[i] = p.Slug
	}
	return strings.Join(slugs, ", ")
}
