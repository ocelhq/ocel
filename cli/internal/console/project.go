package console

import (
	"context"

	"connectrpc.com/connect"

	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

func (c *Client) projects(accessToken string) consolev1connect.ProjectServiceClient {
	return consolev1connect.NewProjectServiceClient(c.http, c.baseURL+connectRoute, c.withSessionHeaders(accessToken))
}

func IsConflict(err error) bool {
	return connect.CodeOf(err) == connect.CodeAlreadyExists
}

func (c *Client) ListProjects(ctx context.Context, accessToken string) ([]*consolev1.Project, error) {
	listed, err := c.projects(accessToken).List(ctx, &consolev1.ListProjectsRequest{})
	if err != nil {
		return nil, err
	}
	return listed.GetProjects(), nil
}

func (c *Client) CreateProject(ctx context.Context, accessToken, name, slug string) (*consolev1.Project, error) {
	created, err := c.projects(accessToken).Create(ctx, &consolev1.CreateProjectRequest{Name: name, Slug: slug})
	if err != nil {
		return nil, err
	}
	return created.GetProject(), nil
}
