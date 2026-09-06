package project

import (
	"context"
	"net/http"

	"github.com/ocelhq/ocel/cli/internal/console/httpapi"
)

type Project struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organizationId"`
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	Description    *string `json:"description"`
}

type Client struct {
	api *httpapi.Client
}

func New(baseURL string) *Client {
	return &Client{api: httpapi.New(baseURL)}
}

func IsConflict(err error) bool {
	return httpapi.HasStatus(err, http.StatusConflict)
}

func IsUnauthorized(err error) bool {
	return httpapi.HasStatus(err, http.StatusUnauthorized)
}

func (c *Client) ListProjects(ctx context.Context, accessToken string) ([]Project, error) {
	var projects []Project
	if err := c.api.Get(ctx, "/api/projects", accessToken, &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

func (c *Client) CreateProject(ctx context.Context, accessToken, name, slug string) (*Project, error) {
	var project Project
	body := map[string]string{"name": name, "slug": slug}
	if err := c.api.Post(ctx, "/api/projects", accessToken, body, &project); err != nil {
		return nil, err
	}
	return &project, nil
}
