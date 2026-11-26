package link

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/prompt"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/progress"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
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
	return commands.DeclareResult(commands.DeclareMutating(commands.ReserveStdout(cmd)), &resultv1.LinkResult{})
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
	projectRef = strings.TrimSpace(projectRef)

	if existing, err := console.ReadLink(projectDir, apiURL); err != nil {
		return err
	} else if existing != nil {
		linking.Phase(progressv1.Phase_PHASE_CHECK).Say(fmt.Sprintf("This directory is linked to %s now; linking it again", existing.ProjectName))
	}

	account := commands.NewConsoleAccount(apiURL, creds.AccessToken)
	lr := linkRun{run: linking, prompt: prompt.New(stderr, stdin), canAsk: dependencies.CanAsk(stdin)}

	org, err := pickOrganization(ctx, lr, account, opts)
	if err != nil {
		return err
	}

	var projects []*consolev1.Project
	err = lr.wait(ctx, progressv1.Phase_PHASE_CHECK, org.Slug, progress.Loading.Title("the projects in "+org.Name), func(ctx context.Context) (err error) {
		projects, err = account.ListProjects(ctx, org)
		return err
	})
	if err != nil {
		return err
	}

	selected, err := selectOrCreateProject(ctx, lr, account, projectDir, projectRef, opts, projects, org)
	if err != nil {
		return err
	}

	if _, err := account.WriteLink(projectDir, org, selected); err != nil {
		return err
	}

	linking.Succeed(commands.LinkedMessage(org, selected))
	if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
		return terminal.WriteResultJSON(stdout, &resultv1.LinkResult{
			Organization: &resultv1.ConsoleOrganization{Id: org.ID, Name: org.Name, Slug: org.Slug},
			Project:      &resultv1.ConsoleProject{Id: selected.GetId(), Name: selected.GetName(), Slug: selected.GetSlug()},
		})
	}
	return nil
}

type linkRun struct {
	run    *run.Run
	prompt prompt.Prompt
	canAsk bool
}

func (c linkRun) wait(ctx context.Context, phase progressv1.Phase, subject string, title progress.Title, fn func(context.Context) error) error {
	span := c.run.Phase(phase)
	ctx, child := span.Child(ctx, subject, title)
	err := fn(ctx)
	child.End(err)
	span.End(err)
	return err
}

func (c linkRun) choose(ctx context.Context, title string, options []prompt.Option) (choice string, answered bool, err error) {
	err = c.run.Ask(func() error {
		choice, answered, err = c.prompt.Select(ctx, title, options)
		return err
	})
	return choice, answered, err
}

func (c linkRun) input(ctx context.Context, title, description string) (typed string, answered bool, err error) {
	err = c.run.Ask(func() error {
		typed, answered, err = c.prompt.Input(ctx, title, description)
		return err
	})
	return typed, answered, err
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
	account commands.ConsoleAccount,
	projectDir, projectRef string,
	opts options,
	projects []*consolev1.Project,
	org *console.Organization,
) (*consolev1.Project, error) {
	create := func(name string) (*consolev1.Project, error) {
		return createProject(ctx, lr, account, name, org)
	}

	if opts.create {
		return create(defaultProjectName(projectDir, projectRef))
	}

	if projectRef != "" {
		if found := commands.FindProject(projects, projectRef); found != nil {
			return found, nil
		}
		if len(projects) == 0 {
			return nil, fmt.Errorf("%s has no projects yet — run `ocel link --create` to make one", org.Name)
		}
		return nil, fmt.Errorf("no project with slug %q in %s; available: %s (or pass --create)", projectRef, org.Name, commands.ProjectSlugs(projects))
	}

	if !lr.canAsk {
		if len(projects) == 0 {
			return nil, clierror.NewInputRequired(errors.New("no project selected — pass --create to make one"), "--create")
		}
		return nil, clierror.NewInputRequired(
			fmt.Errorf("no project selected — pass a project slug or --create. available: %s", commands.ProjectSlugs(projects)),
			"ocel link <project>",
		)
	}

	if len(projects) == 0 {
		name, err := promptProjectName(ctx, lr, projectDir, fmt.Sprintf("%s has no projects yet; name the new one", org.Name))
		if err != nil {
			return nil, err
		}
		return create(name)
	}

	const createNew = ""
	options := make([]prompt.Option, 0, len(projects)+1)
	for _, p := range projects {
		options = append(options, prompt.Option{Name: p.GetSlug(), Label: fmt.Sprintf("%s (%s)", p.GetName(), p.GetSlug())})
	}
	options = append(options, prompt.Option{Name: createNew, Label: "Create a new project"})
	selection, answered, err := lr.choose(ctx, fmt.Sprintf("Project in %s to link", org.Name), options)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}
	if !answered {
		return nil, errors.New("no project selected; rerun `ocel link`")
	}
	if selection == createNew {
		name, err := promptProjectName(ctx, lr, projectDir, "Name of the new project")
		if err != nil {
			return nil, err
		}
		return create(name)
	}
	return commands.FindProject(projects, selection), nil
}

func createProject(ctx context.Context, lr linkRun, account commands.ConsoleAccount, name string, org *console.Organization) (*consolev1.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("project name required — pass it as an argument, e.g. `ocel link --create my-app`")
	}
	projectSlug := project.DeriveSlug(name)
	if projectSlug == "" {
		return nil, fmt.Errorf("could not derive a valid slug from %q — try a name with at least one alphanumeric character", name)
	}

	var created *consolev1.Project
	err := lr.wait(ctx, progressv1.Phase_PHASE_PROVISION, projectSlug, progress.Creating.Title("the project in "+org.Name), func(ctx context.Context) error {
		p, createErr := account.Client.CreateProject(ctx, account.Session, name, projectSlug)
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

func promptProjectName(ctx context.Context, lr linkRun, projectDir, title string) (string, error) {
	fallback := filepath.Base(projectDir)
	name, answered, err := lr.input(ctx, title, "Enter takes "+fallback)
	if err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	if !answered {
		return "", errors.New("no project name given; rerun `ocel link`")
	}
	if name == "" {
		return fallback, nil
	}
	return name, nil
}

func pickOrganization(ctx context.Context, lr linkRun, account commands.ConsoleAccount, opts options) (*console.Organization, error) {
	var orgs []console.Organization
	err := lr.wait(ctx, progressv1.Phase_PHASE_CHECK, consoleHost(account.URL), progress.Loading.Title("your organizations"), func(ctx context.Context) (err error) {
		orgs, err = account.ListOrganizations(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}

	bySlug := func(slug string) func(console.Organization) bool {
		return func(org console.Organization) bool { return org.Slug == slug }
	}
	if opts.org != "" {
		if org := commands.FindOrganization(orgs, bySlug(opts.org)); org != nil {
			return org, nil
		}
		return nil, fmt.Errorf("no organization with slug %q found; available: %s", opts.org, commands.OrganizationSlugs(orgs))
	}

	if len(orgs) == 1 {
		return &orgs[0], nil
	}

	if !lr.canAsk {
		return nil, clierror.NewInputRequired(
			fmt.Errorf("multiple organizations found; pass --org <slug>. available: %s", commands.OrganizationSlugs(orgs)),
			"--org <slug>",
		)
	}

	options := make([]prompt.Option, 0, len(orgs))
	for _, org := range orgs {
		options = append(options, prompt.Option{Name: org.Slug, Label: fmt.Sprintf("%s (%s)", org.Name, org.Slug)})
	}
	selection, answered, err := lr.choose(ctx, "Organization to link", options)
	if err != nil {
		return nil, fmt.Errorf("failed to read input: %w", err)
	}
	if !answered {
		return nil, errors.New("no organization selected; rerun `ocel link`")
	}
	return commands.FindOrganization(orgs, bySlug(selection)), nil
}
