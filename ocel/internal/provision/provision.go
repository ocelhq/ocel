// Package provision fetches project identity and resolves declared
// resources to live connections.
//
// FetchProjectConfig is still stubbed until the real Ocel API exists for
// project/org identity. Provision is real: it calls POST
// {apiURL}/api/resources/resolve, the same endpoint
// packages/api/src/routes/resources/resolve/route.ts serves in prod, applying
// the on-disk resolve cache (see CachedResolve). Both entry points'
// signatures are final: the rest of the CLI (see internal/cli.devCmd) depends
// only on these shapes, so wiring in the real FetchProjectConfig later
// requires no caller changes.
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
	"github.com/ocelhq/ocel/internal/resolvecache"
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
// has no per-instance state to hang one off of.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// openCache opens the on-disk resolve cache CachedResolve reads and writes.
// A var so tests can point it at a temp directory instead of Open's real
// user config dir.
var openCache = resolvecache.Open

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
// the resolve endpoint at cfg.APIURL, reusing a cached response when one is
// available (see CachedResolve).
func Provision(ctx context.Context, cfg ProjectConfig, resources []manifest.Entry) ([]ProvisionedResource, error) {
	return CachedResolve(ctx, httpClient, cfg.APIURL, cfg.Token, cfg.ProjectID, resources)
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
	ExpiresAt string            `json:"expiresAt"`
}

// Resolve calls POST {baseURL}/api/resources/resolve - the endpoint
// packages/api/src/routes/resources/resolve/route.ts serves - and translates
// its flat env-map response back into one ProvisionedResource per requested
// resource. It always calls the API; callers that want the on-disk resolve
// cache applied should use CachedResolve instead.
func Resolve(ctx context.Context, client *http.Client, baseURL, token, projectID string, resources []manifest.Entry) ([]ProvisionedResource, error) {
	if len(resources) == 0 {
		return []ProvisionedResource{}, nil
	}

	env, _, err := callResolve(ctx, client, baseURL, token, projectID, resources)
	if err != nil {
		return nil, err
	}
	return resourcesFromEnv(resources, env)
}

// CachedResolve wraps Resolve with the on-disk cache in internal/resolvecache:
// when the sorted resource definitions and an account fingerprint (baseURL +
// token) match the last cached resolve response for projectID, and its
// server-provided expiresAt hasn't passed, it reuses that response instead of
// calling the API. Otherwise it calls Resolve and restashes the fresh
// response. Provision uses this.
func CachedResolve(ctx context.Context, client *http.Client, baseURL, token, projectID string, resources []manifest.Entry) ([]ProvisionedResource, error) {
	if len(resources) == 0 {
		return []ProvisionedResource{}, nil
	}

	defs := make([]resolvecache.Def, 0, len(resources))
	for _, r := range resources {
		typeName, err := ResourceTypeName(r.Type)
		if err != nil {
			return nil, err
		}
		defs = append(defs, resolvecache.Def{Name: r.Name, Type: typeName})
	}
	defsHash := resolvecache.HashDefs(defs)
	account := resolvecache.Fingerprint(baseURL, token)

	cache, cacheErr := openCache()
	if cacheErr == nil {
		if entry, ok := cache.Load(projectID); ok &&
			entry.DefsHash == defsHash &&
			entry.Account == account &&
			time.Now().Before(entry.ExpiresAt) {
			return resourcesFromEnv(resources, entry.Env)
		}
	}

	env, expiresAt, err := callResolve(ctx, client, baseURL, token, projectID, resources)
	if err != nil {
		return nil, err
	}

	// Caching is best-effort: a cache we can't open or write to shouldn't
	// fail a resolve that otherwise succeeded.
	if cacheErr == nil && !expiresAt.IsZero() {
		_ = cache.Save(projectID, resolvecache.Entry{
			DefsHash:  defsHash,
			Account:   account,
			ExpiresAt: expiresAt,
			Env:       env,
		})
	}

	return resourcesFromEnv(resources, env)
}

// callResolve performs the POST /api/resources/resolve request and returns
// its decoded env map and expiry.
func callResolve(ctx context.Context, client *http.Client, baseURL, token, projectID string, resources []manifest.Entry) (map[string]string, time.Time, error) {
	entries := make([]resolveResourceEntry, 0, len(resources))
	for _, r := range resources {
		typeName, err := ResourceTypeName(r.Type)
		if err != nil {
			return nil, time.Time{}, err
		}
		entries = append(entries, resolveResourceEntry{Name: r.Name, Type: typeName})
	}

	body, err := json.Marshal(resolveRequestBody{ProjectID: projectID, Resources: entries})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("encode resolve request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/resources/resolve", bytes.NewReader(body))
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("build resolve request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("resolve resources: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("resolve resources: unexpected status %d", resp.StatusCode)
	}

	var decoded resolveResponseBody
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, time.Time{}, fmt.Errorf("decode resolve response: %w", err)
	}

	// A missing/malformed expiresAt just disables caching for this response
	// (zero time never satisfies CachedResolve's time.Now().Before check);
	// it shouldn't fail the resolve itself.
	expiresAt, _ := time.Parse(time.RFC3339, decoded.ExpiresAt)

	return decoded.Env, expiresAt, nil
}

// resourcesFromEnv translates a flat OCEL_RESOURCE_<TYPE>_<name> -> value env
// map into one ProvisionedResource per requested resource.
func resourcesFromEnv(resources []manifest.Entry, env map[string]string) ([]ProvisionedResource, error) {
	out := make([]ProvisionedResource, 0, len(resources))
	for _, r := range resources {
		typeName, err := ResourceTypeName(r.Type)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("OCEL_RESOURCE_%s_%s", typeName, r.Name)
		value, ok := env[key]
		if !ok {
			return nil, fmt.Errorf("resolve response missing env for resource %q", r.Name)
		}
		out = append(out, ProvisionedResource{Name: r.Name, Type: r.Type, Env: map[string]string{key: value}})
	}
	return out, nil
}

// ResourceTypeName renders a ResourceType as it appears in
// OCEL_RESOURCE_<TYPE>_<name>, matching the SDK's getConfig (see
// packages/ocel/src/utils/get-config.ts).
func ResourceTypeName(t resourcesv1.ResourceType) (string, error) {
	if t == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return "", fmt.Errorf("resource has unspecified type")
	}
	return strings.TrimPrefix(t.String(), "RESOURCE_TYPE_"), nil
}
