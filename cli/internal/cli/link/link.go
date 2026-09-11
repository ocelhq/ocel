package link

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

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/console/auth"
	consolelink "github.com/ocelhq/ocel/cli/internal/console/link"
	"github.com/ocelhq/ocel/cli/internal/console/project"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/cli/internal/slug"
)

type options struct {
	org    string
	create bool
	apiURL string
}

func NewCommand(deps cmddeps.Deps) *cobra.Command {
	var opts options
	cmd := &cobra.Command{
		Use:   "link [project]",
		Short: "Link this directory to a console project",
		Example: "  $ ocel link\n" +
			"  $ ocel link my-app\n" +
			"  $ ocel link my-app --org acme\n" +
			"  $ ocel link --create \"My App\"",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("determine working directory: %w", err)
			}
			dir, err := projectDir(cmd.Context(), deps, cwd)
			if err != nil {
				return err
			}
			projectRef := ""
			if len(args) > 0 {
				projectRef = args[0]
			}
			opts := opts
			creds, _ := deps.LoadCredentials()
			opts.apiURL = console.EffectiveBaseURL(creds.APIURL)
			return run(cmd.Context(), deps, dir, projectRef, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	cmd.Flags().StringVar(&opts.org, "org", "", "Organization `slug`, instead of picking one")
	cmd.Flags().BoolVar(&opts.create, "create", false, "Create the project, named [project] or after this directory")
	return cmd
}

var (
	check = color.New(color.FgGreen).Sprint("✓")
	bold  = color.New(color.Bold).SprintFunc()
)

func run(ctx context.Context, deps cmddeps.Deps, projectDir, projectRef string, opts options, stdout, stderr io.Writer, stdin io.Reader) error {
	creds, err := deps.LoadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &exitsig.ExitError{Code: 1}
	}

	apiURL := strings.TrimRight(opts.apiURL, "/")
	// TODO: link installs no interrupt handler, so SIGINT still hard-kills here —
	// migrating these reads to the prompt package without also installing deps.Interrupt
	// would look like a cleanup but would reintroduce the raw-mode/masked-SIGINT bug
	// the other commands fixed (see #245).
	scanner := bufio.NewScanner(stdin)
	projectRef = strings.TrimSpace(projectRef)

	if existing, err := consolelink.Read(projectDir, apiURL); err != nil {
		return err
	} else if existing != nil {
		fmt.Fprintf(stdout, "Re-linking (currently %s)\n", existing.ProjectName)
	}

	authClient := auth.New(apiURL)
	projectClient := project.New(apiURL)

	org, err := pickOrganization(ctx, deps, authClient, creds.AccessToken, opts, stdout, stdin, scanner)
	if err != nil {
		return err
	}
	if err := authClient.SetActiveOrganization(ctx, creds.AccessToken, org.ID); err != nil {
		return fmt.Errorf("failed to set active organization: %w", err)
	}

	var projects []project.Project
	err = withSpinner(deps, stdout, "Loading projects…", func() error {
		list, listErr := projectClient.ListProjects(ctx, creds.AccessToken)
		projects = list
		return listErr
	})
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}

	selected, err := selectOrCreateProject(ctx, deps, projectClient, creds.AccessToken, projectDir, projectRef, opts, projects, org, stdout, stdin, scanner)
	if err != nil {
		return err
	}

	if err := consolelink.Write(projectDir, consolelink.Link{
		APIURL:         apiURL,
		OrganizationID: org.ID,
		ProjectID:      selected.ID,
		ProjectName:    selected.Name,
	}); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "%s Linked to %s in %s\n", check, bold(selected.Slug), org.Name)
	return nil
}

func Ensure(ctx context.Context, deps cmddeps.Deps, projectDir, apiURL string, stdout, stderr io.Writer, stdin io.Reader) (*consolelink.Link, error) {
	existing, err := consolelink.Read(projectDir, apiURL)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	if !prompt.Interactive(stdin) {
		return nil, fmt.Errorf("%s isn't linked to a console project — run `ocel link <project>` (or `ocel link --create`) first", projectDir)
	}

	fmt.Fprintln(stdout, "This directory isn't linked to a console project yet.")
	if err := run(ctx, deps, projectDir, "", options{apiURL: apiURL}, stdout, stderr, stdin); err != nil {
		return nil, err
	}

	existing, err = consolelink.Read(projectDir, apiURL)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, errors.New("linking recorded no project — run `ocel link` and try again")
	}
	return existing, nil
}

func selectOrCreateProject(
	ctx context.Context,
	deps cmddeps.Deps,
	client *project.Client,
	accessToken, projectDir, projectRef string,
	opts options,
	projects []project.Project,
	org *auth.Organization,
	stdout io.Writer,
	stdin io.Reader,
	scanner *bufio.Scanner,
) (*project.Project, error) {
	create := func(name string) (*project.Project, error) {
		return createProject(ctx, deps, client, accessToken, name, org, stdout)
	}

	if opts.create {
		return create(defaultProjectName(projectDir, projectRef))
	}

	if projectRef != "" {
		for i := range projects {
			if projects[i].Slug == projectRef {
				return &projects[i], nil
			}
		}
		if len(projects) == 0 {
			return nil, fmt.Errorf("%s has no projects yet — run `ocel link --create` to make one", org.Name)
		}
		return nil, fmt.Errorf("no project with slug %q in %s; available: %s (or pass --create)", projectRef, org.Name, joinProjectSlugs(projects))
	}

	if !prompt.Interactive(stdin) {
		if len(projects) == 0 {
			return nil, errors.New("no project selected — pass --create to make one")
		}
		return nil, fmt.Errorf("no project selected — pass a project slug or --create. available: %s", joinProjectSlugs(projects))
	}

	if len(projects) == 0 {
		fmt.Fprintf(stdout, "%s has no projects yet.\n", org.Name)
		return create(promptProjectName(projectDir, stdout, scanner))
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
		return nil, errors.New("no project selected; rerun `ocel link`")
	case strings.EqualFold(selection, "n"):
		return create(promptProjectName(projectDir, stdout, scanner))
	}

	if idx, convErr := strconv.Atoi(selection); convErr == nil {
		if idx < 1 || idx > len(projects) {
			return nil, fmt.Errorf("invalid selection %q; rerun `ocel link`", selection)
		}
		return &projects[idx-1], nil
	}
	for i := range projects {
		if projects[i].Slug == selection {
			return &projects[i], nil
		}
	}
	return nil, fmt.Errorf("invalid selection %q; rerun `ocel link`", selection)
}

func createProject(ctx context.Context, deps cmddeps.Deps, client *project.Client, accessToken, name string, org *auth.Organization, stdout io.Writer) (*project.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project name required — pass it as an argument, e.g. `ocel link --create my-app`")
	}
	projectSlug := slug.From(name)
	if projectSlug == "" {
		return nil, fmt.Errorf("could not derive a valid slug from %q — try a name with at least one alphanumeric character", name)
	}

	var created *project.Project
	err := withSpinner(deps, stdout, fmt.Sprintf("Creating %s…", projectSlug), func() error {
		p, createErr := client.CreateProject(ctx, accessToken, name, projectSlug)
		created = p
		return createErr
	})
	if err != nil {
		if project.IsConflict(err) {
			return nil, fmt.Errorf("a project with slug %q already exists in %s — run `ocel link %s` to link to it", projectSlug, org.Name, projectSlug)
		}
		return nil, fmt.Errorf("failed to create project: %w", err)
	}
	fmt.Fprintf(stdout, "%s Created %s\n", check, created.Name)
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

func pickOrganization(ctx context.Context, deps cmddeps.Deps, client *auth.Client, accessToken string, opts options, stdout io.Writer, stdin io.Reader, scanner *bufio.Scanner) (*auth.Organization, error) {
	var orgs []auth.Organization
	err := withSpinner(deps, stdout, "Loading organizations…", func() error {
		list, listErr := client.ListOrganizations(ctx, accessToken)
		orgs = list
		return listErr
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list organizations: %w", err)
	}

	if len(orgs) == 0 {
		return nil, errors.New("you don't belong to any organization yet — create one in the Ocel console first")
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

	if !prompt.Interactive(stdin) {
		return nil, fmt.Errorf("multiple organizations found; pass --org <slug>. available: %s", joinOrgSlugs(orgs))
	}

	fmt.Fprintln(stdout, "Organizations:")
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
		return nil, errors.New("no organization selected; rerun `ocel link`")
	}

	if idx, convErr := strconv.Atoi(selection); convErr == nil {
		if idx < 1 || idx > len(orgs) {
			return nil, fmt.Errorf("invalid selection %q; rerun `ocel link`", selection)
		}
		return &orgs[idx-1], nil
	}
	for i := range orgs {
		if orgs[i].Slug == selection {
			return &orgs[i], nil
		}
	}
	return nil, fmt.Errorf("invalid selection %q; rerun `ocel link`", selection)
}

func withSpinner(deps cmddeps.Deps, stdout io.Writer, label string, fn func() error) error {
	present := deps.Presentation(stdout)
	if !present.TTY {
		return fn()
	}
	s := runui.StartSpinner(present, stdout, label)
	defer s.Stop()
	return fn()
}

func joinOrgSlugs(orgs []auth.Organization) string {
	slugs := make([]string, len(orgs))
	for i, org := range orgs {
		slugs[i] = org.Slug
	}
	return strings.Join(slugs, ", ")
}

func joinProjectSlugs(projects []project.Project) string {
	slugs := make([]string, len(projects))
	for i, p := range projects {
		slugs[i] = p.Slug
	}
	return strings.Join(slugs, ", ")
}
