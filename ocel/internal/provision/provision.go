// Package provision fetches project identity and resolves declared
// resources to live connections.
//
// FetchProjectConfig is still stubbed until the real Ocel API exists for
// project/org identity. Provision is real: it calls POST
// {apiURL}/api/resources/resolve, the same endpoint
// packages/api/src/routes/resources/resolve/route.ts serves in prod and the
// local dev harness mounts verbatim (see internal/localharness.Client,
// which shares this package's Resolve to speak the identical wire
// protocol against the harness instead of the real API). Both entry
// points' signatures are final: the rest of the CLI (see
// internal/cli.devCmd) depends only on these shapes, so wiring in the real
// FetchProjectConfig later requires no caller changes.
package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ocelhq/ocel/internal/manifest"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// ProjectConfig is the project identity and environment fetched from the
// Ocel API for the authenticated user.
type ProjectConfig struct {
	OrgID     string
	ProjectID string
	UserID    string
	EnvVars   map[string]string
	// APIURL and Token round-trip the values FetchProjectConfig was called
	// with, so Provision (which only receives a ProjectConfig, not the
	// original apiURL/token) can still reach the resolve endpoint.
	APIURL string
	Token  string
}

// ProvisionedResource is a single resolved resource, ready to inject into a
// child process's environment.
type ProvisionedResource struct {
	Name string
	Type resourcesv1.ResourceType
	// Env holds ready-to-inject OCEL_RESOURCE_<TYPE>_<name> -> JSON
	// {connectionString} entries.
	Env map[string]string
}

// httpClient is used for every Resolve call. Package-level since Provision
// (unlike localharness.Client, which owns its own *http.Client) has no
// per-instance state to hang one off of.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// FetchProjectConfig fetches the org/project/user identity and project-level
// environment for projectID. Stubbed: real implementation will call the
// Ocel API with apiURL and token for identity; APIURL and Token are real,
// carried through so Provision can resolve against apiURL.
func FetchProjectConfig(_ context.Context, apiURL, token, projectID string) (ProjectConfig, error) {
	return ProjectConfig{
		OrgID:     "org_stub",
		ProjectID: projectID,
		UserID:    "user_stub",
		EnvVars:   map[string]string{},
		APIURL:    apiURL,
		Token:     token,
	}, nil
}

// Provision resolves each declared resource to a live connection by calling
// the resolve endpoint at cfg.APIURL.
func Provision(ctx context.Context, cfg ProjectConfig, resources []manifest.Entry) ([]ProvisionedResource, error) {
	return Resolve(ctx, httpClient, cfg.APIURL, cfg.Token, cfg.ProjectID, resources)
}

type resolveResourceEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type resolveRequestBody struct {
	ProjectID string                 `json:"projectId"`
	Resources []resolveResourceEntry `json:"resources"`
}

type resolveResponseBody struct {
	Env       map[string]string `json:"env"`
	ExpiresAt string             `json:"expiresAt"`
}

// Resolve calls POST {baseURL}/api/resources/resolve - the endpoint
// packages/api/src/routes/resources/resolve/route.ts serves, whether
// baseURL is the real Ocel API or a locally spawned harness mounting the
// same handler - and translates its flat env-map response back into one
// ProvisionedResource per requested resource. Exported so
// internal/localharness.Client can reuse this exact wire protocol instead
// of duplicating it.
func Resolve(ctx context.Context, client *http.Client, baseURL, token, projectID string, resources []manifest.Entry) ([]ProvisionedResource, error) {
	if len(resources) == 0 {
		return []ProvisionedResource{}, nil
	}

	entries := make([]resolveResourceEntry, 0, len(resources))
	for _, r := range resources {
		typeName, err := ResourceTypeName(r.Type)
		if err != nil {
			return nil, err
		}
		entries = append(entries, resolveResourceEntry{Name: r.Name, Type: typeName})
	}

	body, err := json.Marshal(resolveRequestBody{ProjectID: projectID, Resources: entries})
	if err != nil {
		return nil, fmt.Errorf("encode resolve request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/resources/resolve", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build resolve request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolve resources: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("resolve resources: unexpected status %d", resp.StatusCode)
	}

	var decoded resolveResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode resolve response: %w", err)
	}

	out := make([]ProvisionedResource, 0, len(resources))
	for _, r := range resources {
		typeName, _ := ResourceTypeName(r.Type) // already validated above
		key := fmt.Sprintf("OCEL_RESOURCE_%s_%s", typeName, r.Name)
		value, ok := decoded.Env[key]
		if !ok {
			return nil, fmt.Errorf("resolve response missing env for resource %q", r.Name)
		}
		out = append(out, ProvisionedResource{Name: r.Name, Type: r.Type, Env: map[string]string{key: value}})
	}
	return out, nil
}

// ResourceTypeName renders a ResourceType as it appears in
// OCEL_RESOURCE_<TYPE>_<name>, matching the SDK's getConfig (see
// packages/ocel/src/utils/get-config.ts). Exported for reuse by callers that
// need to speak the same wire format, e.g. internal/localharness.
func ResourceTypeName(t resourcesv1.ResourceType) (string, error) {
	if t == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return "", fmt.Errorf("resource has unspecified type")
	}
	return strings.TrimPrefix(t.String(), "RESOURCE_TYPE_"), nil
}
