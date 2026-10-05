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

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type options struct {
	org    string
	create bool
	apiURL string
}

type Dependencies struct {
	commands.Invocation
	LoadCredentials func() (console.Credentials, error)
}

func NewCommand(dependencies Dependencies) *cobra.Command {
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
			dir, err := projectDir(cmd.Context(), dependencies, cwd)
			if err != nil {
				return err
			}
			projectRef := ""
			if len(args) > 0 {
				projectRef = args[0]
			}
			opts := opts
			creds, _ := dependencies.LoadCredentials()
			opts.apiURL = console.BaseURL(creds.APIURL)
			return runLink(cmd.Context(), dependencies, dir, projectRef, opts, cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin())
		},
	}
	cmd.Flags().StringVar(&opts.org, "org", "", "Organization `slug`, instead of picking one")
	cmd.Flags().BoolVar(&opts.create, "create", false, "Create the project, named [project] or after this directory")
	return commands.DeclareMutating(commands.ReserveStdout(cmd))
}

func runLink(ctx context.Context, dependencies Dependencies, projectDir, projectRef string, opts options, stdout, stderr io.Writer, stdin io.Reader) (err error) {
	creds, err := console.RequireLogin(dependencies.LoadCredentials, stderr)
	if err != nil {
		return err
	}

	ctx, linking, err := dependencies.Events.Begin(ctx, "ocel link", "")
	if err != nil {
		return err
	}
	defer linking.End(&err)

	apiURL := opts.apiURL
	scanner := bufio.NewScanner(stdin)
	projectRef = strings.TrimSpace(projectRef)

	if existing, err := console.ReadLink(projectDir, apiURL); err != nil {
		return err
	} else if existing != nil {
		linking.Phase(progressv1.Phase_PHASE_CHECK).Say(fmt.Sprintf("This directory is linked to %s now; linking it again", existing.ProjectName))
	}

	client := console.New(apiURL)
	lr := linkRun{run: linking, stderr: stderr, stdin: stdin, scanner: scanner, canAsk: dependencies.CanAsk(stdin)}

	org, err := pickOrganization(ctx, lr, client, creds.AccessToken, apiURL, opts)
	if err != nil {
		return err
	}
	if err := client.SetActiveOrganization(ctx, creds.AccessToken, org.ID); err != nil {
		return fmt.Errorf("failed to set active organization: %w", err)
	}

	var projects []console.Project
	err = lr.wait(progressv1.Phase_PHASE_CHECK, org.Slug, progress.Loading.Title("the projects in "+org.Name), func() error {
		list, listErr := client.ListProjects(ctx, creds.AccessToken)
		projects = list
		return listErr
	})
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}

	selected, err := selectOrCreateProject(ctx, lr, client, creds.AccessToken, projectDir, projectRef, opts, projects, org)
	if err != nil {
		return err
	}

	if err := console.WriteLink(projectDir, console.Link{
		APIURL:         apiURL,
		OrganizationID: org.ID,
		ProjectID:      selected.ID,
		ProjectName:    selected.Name,
	}); err != nil {
		return err
	}

	linking.Succeed(fmt.Sprintf("Linked this directory to %s (%s)", selected.Slug, org.Name))
	if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, &resultv1.LinkResult{
			Organization: &resultv1.ConsoleOrganization{Id: org.ID, Name: org.Name, Slug: org.Slug},
			Project:      &resultv1.ConsoleProject{Id: selected.ID, Name: selected.Name, Slug: selected.Slug},
		})
	}
	return nil
}

type linkRun struct {
	run     *run.Run
	stderr  io.Writer
	stdin   io.Reader
	scanner *bufio.Scanner
	canAsk  bool
}

func (c linkRun) wait(phase progressv1.Phase, subject string, title progress.Title, fn func() error) error {
	span := c.run.Phase(phase)
	child := span.Child(subject, title)
	err := fn()
	child.End(err)
	span.End(err)
	return err
}

func (c linkRun) ask(ctx context.Context, question func(w io.Writer)) (string, error) {
	var answer string
	err := c.run.Ask(func() error {
		question(c.stderr)
		answered := make(chan string, 1)
		go func() {
			line := ""
			if c.scanner.Scan() {
				line = strings.TrimSpace(c.scanner.Text())
			}
			answered <- line
		}()
		select {
		case answer = <-answered:
			return c.scanner.Err()
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	return answer, err
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
	client *console.Client,
	accessToken, projectDir, projectRef string,
	opts options,
	projects []console.Project,
	org *console.Organization,
) (*console.Project, error) {
	create := func(name string) (*console.Project, error) {
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

	if !lr.canAsk {
		if len(projects) == 0 {
			return nil, clierror.NewInputRequired(errors.New("no project selected — pass --create to make one"), "--create")
		}
		return nil, clierror.NewInputRequired(
			fmt.Errorf("no project selected — pass a project slug or --create. available: %s", joinProjectSlugs(projects)),
			"ocel link <project>",
		)
	}

	if len(projects) == 0 {
		name, err := promptProjectName(ctx, lr, projectDir, fmt.Sprintf("%s has no projects yet.\n", org.Name))
		if err != nil {
			return nil, err
		}
		return create(name)
	}

	selection, err := lr.ask(ctx, func(w io.Writer) {
		fmt.Fprintf(w, "Projects in %s:\n", org.Name)
		for i, p := range projects {
			fmt.Fprintf(w, "  %d) %s (%s)\n", i+1, p.Name, p.Slug)
		}
		fmt.Fprintln(w, "  n) Create a new project")
		fmt.Fprint(w, "Select a project (number, slug, or n): ")
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}
	switch {
	case selection == "":
		return nil, errors.New("no project selected; rerun `ocel link`")
	case strings.EqualFold(selection, "n"):
		name, err := promptProjectName(ctx, lr, projectDir, "")
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

func createProject(ctx context.Context, lr linkRun, client *console.Client, accessToken, name string, org *console.Organization) (*console.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project name required — pass it as an argument, e.g. `ocel link --create my-app`")
	}
	projectSlug := project.DeriveSlug(name)
	if projectSlug == "" {
		return nil, fmt.Errorf("could not derive a valid slug from %q — try a name with at least one alphanumeric character", name)
	}

	var created *console.Project
	err := lr.wait(progressv1.Phase_PHASE_PROVISION, projectSlug, progress.Creating.Title("the project in "+org.Name), func() error {
		p, createErr := client.CreateProject(ctx, accessToken, name, projectSlug)
		created = p
		return createErr
	})
	if err != nil {
		if console.IsConflict(err) {
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

func promptProjectName(ctx context.Context, lr linkRun, projectDir, lead string) (string, error) {
	fallback := filepath.Base(projectDir)
	name, err := lr.ask(ctx, func(w io.Writer) {
		fmt.Fprint(w, lead)
		fmt.Fprintf(w, "Project name (%s): ", fallback)
	})
	if err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	if name == "" {
		return fallback, nil
	}
	return name, nil
}

func pickOrganization(ctx context.Context, lr linkRun, client *console.Client, accessToken, apiURL string, opts options) (*console.Organization, error) {
	var orgs []console.Organization
	err := lr.wait(progressv1.Phase_PHASE_CHECK, consoleHost(apiURL), progress.Loading.Title("your organizations"), func() error {
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

	if !lr.canAsk {
		return nil, clierror.NewInputRequired(
			fmt.Errorf("multiple organizations found; pass --org <slug>. available: %s", joinOrgSlugs(orgs)),
			"--org <slug>",
		)
	}

	selection, err := lr.ask(ctx, func(w io.Writer) {
		fmt.Fprintln(w, "Organizations:")
		for i, org := range orgs {
			fmt.Fprintf(w, "  %d) %s (%s)\n", i+1, org.Name, org.Slug)
		}
		fmt.Fprint(w, "Select an organization (number or slug): ")
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

func joinOrgSlugs(orgs []console.Organization) string {
	slugs := make([]string, len(orgs))
	for i, org := range orgs {
		slugs[i] = org.Slug
	}
	return strings.Join(slugs, ", ")
}

func joinProjectSlugs(projects []console.Project) string {
	slugs := make([]string, len(projects))
	for i, p := range projects {
		slugs[i] = p.Slug
	}
	return strings.Join(slugs, ", ")
}
