package console

import (
	"context"
	"net/http"
)

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (c *Client) ListOrganizations(ctx context.Context, accessToken string) ([]Organization, error) {
	var out []Organization
	if err := c.send(ctx, http.MethodGet, "/api/auth/organization/list", accessToken, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SetActiveOrganization(ctx context.Context, accessToken, organizationID string) error {
	body := map[string]string{"organizationId": organizationID}
	return c.send(ctx, http.MethodPost, "/api/auth/organization/set-active", accessToken, body, nil)
}
