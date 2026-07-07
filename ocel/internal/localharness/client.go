package localharness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ocelhq/ocel/internal/manifest"
	"github.com/ocelhq/ocel/internal/provision"
)

// Client speaks a harness process's provisioning-handshake HTTP protocol:
// POST <baseURL>/dev/project-config for the dev-only project-config
// passthrough, and POST <baseURL>/api/resources/resolve for the real
// resolve handler the harness mounts verbatim from packages/api. Its
// methods match the fetchProjectConfig/provision shapes
// devserver.WithProvisioner expects, so a harness can be wired in as a
// drop-in replacement for the real Ocel API.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient returns a Client that talks to a harness process reachable at
// baseURL (e.g. "http://127.0.0.1:PORT"). token is sent as an
// `Authorization: Bearer` header on every request — the harness's dev
// endpoints authenticate through Better Auth's bearer plugin, exactly like
// the real API routes they sit next to.
func NewClient(baseURL, token string) *Client {
	return &Client{baseURL: baseURL, token: token, http: http.DefaultClient}
}

type projectConfigRequest struct {
	APIURL    string `json:"apiUrl"`
	Token     string `json:"token"`
	ProjectID string `json:"projectId"`
}

type projectConfigResponse struct {
	OrgID     string            `json:"orgId"`
	ProjectID string            `json:"projectId"`
	UserID    string            `json:"userId"`
	EnvVars   map[string]string `json:"envVars"`
}

// FetchProjectConfig implements the same shape as provision.FetchProjectConfig,
// against the harness process instead of the real Ocel API.
func (c *Client) FetchProjectConfig(ctx context.Context, apiURL, token, projectID string) (provision.ProjectConfig, error) {
	body, err := json.Marshal(projectConfigRequest{APIURL: apiURL, Token: token, ProjectID: projectID})
	if err != nil {
		return provision.ProjectConfig{}, fmt.Errorf("encode project config request: %w", err)
	}

	var resp projectConfigResponse
	if err := c.post(ctx, "/dev/project-config", body, &resp); err != nil {
		return provision.ProjectConfig{}, err
	}

	return provision.ProjectConfig{
		OrgID:     resp.OrgID,
		ProjectID: resp.ProjectID,
		UserID:    resp.UserID,
		EnvVars:   resp.EnvVars,
		APIURL:    c.baseURL,
		Token:     c.token,
	}, nil
}

// Provision implements the same shape as provision.Provision, delegating to
// provision.CachedResolve so the harness is called with the exact same
// POST /api/resources/resolve wire protocol the real Ocel API serves - the
// harness mounts packages/api's resolveResources handler verbatim at that
// path, so no separate dev-only wire format is needed here - and gets the
// same on-disk resolve cache the real API path does.
func (c *Client) Provision(ctx context.Context, cfg provision.ProjectConfig, resources []manifest.Entry) ([]provision.ProvisionedResource, error) {
	return provision.CachedResolve(ctx, c.http, c.baseURL, c.token, cfg.ProjectID, resources)
}

func (c *Client) post(ctx context.Context, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request for %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: unexpected status %d", path, resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}
