package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/resolvecache"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

type ProjectConfig struct {
	OrgID     string
	ProjectID string
	UserID    string
	EnvVars   map[string]string
	APIURL    string
	Token     string
}

type ProvisionedResource struct {
	Name string
	Type resourcesv1.ResourceType
	Env  map[string]string
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

var openCache = resolvecache.Open

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

func FetchLiveValues(_ context.Context, apiURL, token, projectID string, keys []string) (map[string]string, error) {
	return make(map[string]string, len(keys)), nil
}

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

	expiresAt, _ := time.Parse(time.RFC3339, decoded.ExpiresAt)

	return decoded.Env, expiresAt, nil
}

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

func ResourceTypeName(t resourcesv1.ResourceType) (string, error) {
	if t == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return "", fmt.Errorf("resource has unspecified type")
	}
	return strings.TrimPrefix(t.String(), "RESOURCE_TYPE_"), nil
}
