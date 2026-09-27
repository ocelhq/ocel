package link

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
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
	"github.com/ocelhq/ocel/cli/internal/events"
	"github.com/ocelhq/ocel/cli/internal/exitsig"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/slug"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
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

var check = color.New(color.FgGreen).Sprint("✓")

func run(ctx context.Context, deps cmddeps.Deps, projectDir, projectRef string, opts options, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	creds, err := deps.LoadCredentials()
	if err != nil {
		fmt.Fprintln(stderr, "You're not logged in. Run `ocel login` first.")
		return &exitsig.ExitError{Code: 1}
	}

	ctx, linking, err := deps.Events.Begin(ctx, "ocel link", "")
	if err != nil {
		return err
	}
	defer linking.End(&err)

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
		linking.Phase(progressv1.Phase_PHASE_CHECK).Say(fmt.Sprintf("Re-linking (currently %s)", existing.ProjectName))
	}

	authClient := auth.New(apiURL)
	projectClient := project.New(apiURL)
	lr := linkRun{run: linking, stdout: stdout, stdin: stdin, scanner: scanner}

	org, err := pickOrganization(ctx, lr, authClient, creds.AccessToken, apiURL, opts)
	if err != nil {
		return err
	}
	if err := authClient.SetActiveOrganization(ctx, creds.AccessToken, org.ID); err != nil {
		return fmt.Errorf("failed to set active organization: %w", err)
	}

	var projects []project.Project
	err = lr.wait(progressv1.Phase_PHASE_CHECK, org.Slug, fmt.Sprintf("Loading the projects in %s", org.Name), func() error {
		list, listErr := projectClient.ListProjects(ctx, creds.AccessToken)
		projects = list
		return listErr
	})
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}

	selected, err := selectOrCreateProject(ctx, lr, projectClient, creds.AccessToken, projectDir, projectRef, opts, projects, org)
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

	linking.Finish(fmt.Sprintf("Linked this directory to %s (%s)", selected.Slug, org.Name))
	return nil
}

type linkRun struct {
	run     *events.Run
	stdout  io.Writer
	stdin   io.Reader
	scanner *bufio.Scanner
}

func (c linkRun) wait(phase progressv1.Phase, subject, message string, fn func() error) error {
	scope := c.run.Phase(phase)
	unit := scope.Unit(subject, message)
	err := fn()
	unit.End(err)
	scope.End(err)
	return err
}

func (c linkRun) ask(question func(stdout io.Writer)) (string, error) {
	resume := c.run.Hold(&streamv1.WaitingEvent{})
	defer resume("answered")
	question(c.stdout)
	answer := ""
	if c.scanner.Scan() {
		answer = strings.TrimSpace(c.scanner.Text())
	}
	return answer, c.scanner.Err()
}

func consoleHost(apiURL string) string {
	if u, err := url.Parse(apiURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return apiURL
}

func selectOrCreateProject(
	ctx context.Context,
	lr linkRun,
	client *project.Client,
	accessToken, projectDir, projectRef string,
	opts options,
	projects []project.Project,
	org *auth.Organization,
) (*project.Project, error) {
	create := func(name string) (*project.Project, error) {
		return createProject(ctx, lr, client, accessToken, name, org)
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

	if !prompt.Interactive(lr.stdin) {
		if len(projects) == 0 {
			return nil, errors.New("no project selected — pass --create to make one")
		}
		return nil, fmt.Errorf("no project selected — pass a project slug or --create. available: %s", joinProjectSlugs(projects))
	}

	if len(projects) == 0 {
		name, err := promptProjectName(lr, projectDir, fmt.Sprintf("%s has no projects yet.\n", org.Name))
		if err != nil {
			return nil, err
		}
		return create(name)
	}

	selection, err := lr.ask(func(stdout io.Writer) {
		fmt.Fprintf(stdout, "Projects in %s:\n", org.Name)
		for i, p := range projects {
			fmt.Fprintf(stdout, "  %d) %s (%s)\n", i+1, p.Name, p.Slug)
		}
		fmt.Fprintln(stdout, "  n) Create a new project")
		fmt.Fprint(stdout, "Select a project (number, slug, or n): ")
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}
	switch {
	case selection == "":
		return nil, errors.New("no project selected; rerun `ocel link`")
	case strings.EqualFold(selection, "n"):
		name, err := promptProjectName(lr, projectDir, "")
		if err != nil {
			return nil, err
		}
		return create(name)
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

func createProject(ctx context.Context, lr linkRun, client *project.Client, accessToken, name string, org *auth.Organization) (*project.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project name required — pass it as an argument, e.g. `ocel link --create my-app`")
	}
	projectSlug := slug.From(name)
	if projectSlug == "" {
		return nil, fmt.Errorf("could not derive a valid slug from %q — try a name with at least one alphanumeric character", name)
	}

	var created *project.Project
	err := lr.wait(progressv1.Phase_PHASE_PROVISION, projectSlug, fmt.Sprintf("Creating the project in %s", org.Name), func() error {
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
	return created, nil
}

func defaultProjectName(projectDir, name string) string {
	if name != "" {
		return name
	}
	return filepath.Base(projectDir)
}

func promptProjectName(lr linkRun, projectDir, lead string) (string, error) {
	fallback := filepath.Base(projectDir)
	name, err := lr.ask(func(stdout io.Writer) {
		fmt.Fprint(stdout, lead)
		fmt.Fprintf(stdout, "Project name (%s): ", fallback)
	})
	if err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	if name == "" {
		return fallback, nil
	}
	return name, nil
}

func pickOrganization(ctx context.Context, lr linkRun, client *auth.Client, accessToken, apiURL string, opts options) (*auth.Organization, error) {
	var orgs []auth.Organization
	err := lr.wait(progressv1.Phase_PHASE_CHECK, consoleHost(apiURL), "Loading your organizations", func() error {
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

	if !prompt.Interactive(lr.stdin) {
		return nil, fmt.Errorf("multiple organizations found; pass --org <slug>. available: %s", joinOrgSlugs(orgs))
	}

	selection, err := lr.ask(func(stdout io.Writer) {
		fmt.Fprintln(stdout, "Organizations:")
		for i, org := range orgs {
			fmt.Fprintf(stdout, "  %d) %s (%s)\n", i+1, org.Name, org.Slug)
		}
		fmt.Fprint(stdout, "Select an organization (number or slug): ")
	})
	if err != nil {
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
