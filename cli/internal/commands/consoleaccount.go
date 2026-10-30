package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/progress"

	"github.com/ocelhq/ocel/cli/internal/console"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

type ConsoleAccount struct {
	URL     string
	Client  *console.Client
	Session string
}

func NewConsoleAccount(consoleURL, session string) ConsoleAccount {
	return ConsoleAccount{URL: consoleURL, Client: console.New(consoleURL), Session: session}
}

func (a ConsoleAccount) ListOrganizations(ctx context.Context) ([]console.Organization, error) {
	orgs, err := a.Client.ListOrganizations(ctx, a.Session)
	if err != nil {
		return nil, fmt.Errorf("list your organizations in %s: %w", a.URL, err)
	}
	if len(orgs) == 0 {
		return nil, fmt.Errorf("you belong to no organization in %s yet — create one there first", a.URL)
	}
	return orgs, nil
}

func (a ConsoleAccount) ListProjects(ctx context.Context, org *console.Organization) ([]*consolev1.Project, error) {
	if err := a.Client.SetActiveOrganization(ctx, a.Session, org.ID); err != nil {
		return nil, fmt.Errorf("make %s your active organization in %s: %w", org.Name, a.URL, err)
	}
	projects, err := a.Client.ListProjects(ctx, a.Session)
	if err != nil {
		return nil, fmt.Errorf("list the projects in %s: %w", org.Name, err)
	}
	return projects, nil
}

func (a ConsoleAccount) WriteLink(dir string, org *console.Organization, linked *consolev1.Project) (console.Link, error) {
	link := console.Link{APIURL: a.URL, OrganizationID: org.ID, ProjectID: linked.GetId(), ProjectName: linked.GetName()}
	return link, console.WriteLink(dir, link)
}

func LinkedMessage(org *console.Organization, linked *consolev1.Project) string {
	return fmt.Sprintf("Linked this directory to %s (%s)", linked.GetSlug(), org.Name)
}

func FindOrganization(orgs []console.Organization, matches func(console.Organization) bool) *console.Organization {
	for i := range orgs {
		if matches(orgs[i]) {
			return &orgs[i]
		}
	}
	return nil
}

func FindProject(projects []*consolev1.Project, slug string) *consolev1.Project {
	for _, p := range projects {
		if p.GetSlug() == slug {
			return p
		}
	}
	return nil
}

func OrganizationSlugs(orgs []console.Organization) string {
	slugs := make([]string, len(orgs))
	for i, org := range orgs {
		slugs[i] = org.Slug
	}
	return strings.Join(slugs, ", ")
}

func ProjectSlugs(projects []*consolev1.Project) string {
	slugs := make([]string, len(projects))
	for i, p := range projects {
		slugs[i] = p.GetSlug()
	}
	return strings.Join(slugs, ", ")
}

func pickOrganization(ctx context.Context, account ConsoleAccount, projectDir string) (*console.Organization, error) {
	orgs, err := account.ListOrganizations(ctx)
	if err != nil {
		return nil, err
	}
	link, err := console.ReadLink(projectDir, account.URL)
	if err != nil {
		return nil, err
	}
	if link != nil {
		if org := FindOrganization(orgs, func(org console.Organization) bool { return org.ID == link.OrganizationID }); org != nil {
			return org, nil
		}
		return nil, fmt.Errorf("this directory is linked to %s in an organization you no longer belong to — run `ocel link` to link it again", link.ProjectName)
	}
	if read, err := account.Client.GetSession(ctx, account.Session); err == nil && read != nil {
		if org := FindOrganization(orgs, func(org console.Organization) bool { return org.ID == read.Session.ActiveOrganizationID }); org != nil {
			return org, nil
		}
	}
	if len(orgs) == 1 {
		return &orgs[0], nil
	}
	return nil, clierror.NewInputRequired(
		fmt.Errorf("you belong to %d organizations in %s and none is active — run `ocel link --org <slug>` to pick the one this project deploys under: %s", len(orgs), account.URL, OrganizationSlugs(orgs)),
		"ocel link --org <slug>",
	)
}

func linkProject(ctx context.Context, check *run.Span, account ConsoleAccount, cfg *project.Project, org *console.Organization) (*console.Link, error) {
	if link, err := console.ReadLink(cfg.Dir, account.URL); err != nil || link != nil {
		return link, err
	}
	selected, err := findOrCreateProject(ctx, check, account, cfg.Slug, org)
	if err != nil {
		return nil, err
	}
	link, err := account.WriteLink(cfg.Dir, org, selected)
	if err != nil {
		return nil, err
	}
	check.Say(LinkedMessage(org, selected))
	return &link, nil
}

func findOrCreateProject(ctx context.Context, check *run.Span, account ConsoleAccount, slug string, org *console.Organization) (*consolev1.Project, error) {
	find := func(ctx context.Context) (*consolev1.Project, error) {
		projects, err := account.ListProjects(ctx, org)
		return FindProject(projects, slug), err
	}
	loadingCtx, loading := check.Child(ctx, slug, progress.Loading.Title("the projects in "+org.Name))
	found, err := find(loadingCtx)
	loading.End(err)
	if err != nil || found != nil {
		return found, err
	}
	ctx, creating := check.Child(ctx, slug, progress.Creating.Title("the project in "+org.Name))
	created, err := account.Client.CreateProject(ctx, account.Session, slug, slug)
	if console.IsConflict(err) {
		created, err = find(ctx)
		if err == nil && created == nil {
			err = errors.New("the console reports the project exists but does not list it")
		}
	}
	if err != nil {
		err = fmt.Errorf("create the project %s in %s: %w", slug, org.Name, err)
	}
	creating.End(err)
	return created, err
}
