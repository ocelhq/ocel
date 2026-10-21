package commands

import (
	"context"
	"fmt"
	"strings"

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
