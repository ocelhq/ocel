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

type Resource struct {
	Name string
	Type resourcesv1.ResourceType
	Env  map[string]string
}

type Resolver struct {
	HTTP      *http.Client
	OpenCache func() (*resolvecache.Cache, error)
	Now       func() time.Time
}

func NewResolver() *Resolver {
	return &Resolver{
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		OpenCache: resolvecache.Open,
		Now:       time.Now,
	}
}

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

func Run(ctx context.Context, cfg ProjectConfig, resources []manifest.Entry) ([]Resource, error) {
	return NewResolver().Resolve(ctx, cfg.APIURL, cfg.Token, cfg.ProjectID, resources)
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

func (r *Resolver) Resolve(ctx context.Context, baseURL, token, projectID string, resources []manifest.Entry) ([]Resource, error) {
	if len(resources) == 0 {
		return []Resource{}, nil
	}

	defs := make([]resolvecache.Def, 0, len(resources))
	for _, entry := range resources {
		typeName, err := ResourceTypeName(entry.Type)
		if err != nil {
			return nil, err
		}
		defs = append(defs, resolvecache.Def{Name: entry.Name, Type: typeName})
	}
	defsHash := resolvecache.HashDefs(defs)
	account := resolvecache.Fingerprint(baseURL, token)

	cache, cacheErr := r.OpenCache()
	if cacheErr == nil {
		if entry, ok := cache.Load(projectID); ok &&
			entry.DefsHash == defsHash &&
			entry.Account == account &&
			r.Now().Before(entry.ExpiresAt) {
			return resourcesFromEnv(resources, entry.Env)
		}
	}

	env, expiresAt, err := r.callResolve(ctx, baseURL, token, projectID, resources)
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

func (r *Resolver) callResolve(ctx context.Context, baseURL, token, projectID string, resources []manifest.Entry) (map[string]string, time.Time, error) {
	entries := make([]resolveResourceEntry, 0, len(resources))
	for _, resource := range resources {
		typeName, err := ResourceTypeName(resource.Type)
		if err != nil {
			return nil, time.Time{}, err
		}
		entries = append(entries, resolveResourceEntry{Name: resource.Name, Type: typeName})
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

	resp, err := r.HTTP.Do(req)
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

func resourcesFromEnv(resources []manifest.Entry, env map[string]string) ([]Resource, error) {
	out := make([]Resource, 0, len(resources))
	for _, resource := range resources {
		typeName, err := ResourceTypeName(resource.Type)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("OCEL_RESOURCE_%s_%s", typeName, resource.Name)
		value, ok := env[key]
		if !ok {
			return nil, fmt.Errorf("resolve response missing env for resource %q", resource.Name)
		}
		out = append(out, Resource{Name: resource.Name, Type: resource.Type, Env: map[string]string{key: value}})
	}
	return out, nil
}

func ResourceTypeName(t resourcesv1.ResourceType) (string, error) {
	if t == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return "", fmt.Errorf("resource has unspecified type")
	}
	return strings.TrimPrefix(t.String(), "RESOURCE_TYPE_"), nil
}
