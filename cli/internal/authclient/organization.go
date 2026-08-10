package authclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (c *Client) getJSON(ctx context.Context, path, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "ocel-cli")

	return c.do(req, out)
}

func (c *Client) ListOrganizations(ctx context.Context, accessToken string) ([]Organization, error) {
	var out []Organization
	if err := c.getJSON(ctx, "/api/auth/organization/list", accessToken, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) SetActiveOrganization(ctx context.Context, accessToken, organizationID string) error {
	payload, err := json.Marshal(map[string]string{"organizationId": organizationID})
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/auth/organization/set-active", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "ocel-cli")

	return c.do(req, nil)
}
