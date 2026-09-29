package console

import (
	"context"
	"net/http"
)

type Project struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organizationId"`
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	Description    *string `json:"description"`
}

const projectsRoute = "/api/projects"

func IsConflict(err error) bool {
	return hasStatus(err, http.StatusConflict)
}

func (c *Client) ListProjects(ctx context.Context, accessToken string) ([]Project, error) {
	var projects []Project
	if err := c.send(ctx, http.MethodGet, projectsRoute, accessToken, nil, &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func (c *Client) CreateProject(ctx context.Context, accessToken, name, slug string) (*Project, error) {
	var project Project
	body := map[string]string{"name": name, "slug": slug}
	if err := c.send(ctx, http.MethodPost, projectsRoute, accessToken, body, &project); err != nil {
		return nil, err
	}
	return &project, nil
}
