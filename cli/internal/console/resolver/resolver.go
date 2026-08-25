package resolver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ocelhq/ocel/cli/internal/console/resolvecache"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/cli/internal/resourceregistry"
	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

type Resolver struct {
	HTTP      *http.Client
	OpenCache func() (*resolvecache.Cache, error)
	Now       func() time.Time
}

func New() *Resolver {
	return &Resolver{
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		OpenCache: resolvecache.Open,
		Now:       time.Now,
	}
}

func Resolve(ctx context.Context, cfg resolve.Account, resources []resourceregistry.Entry) ([]resolve.Resource, error) {
	return New().Resolve(ctx, cfg.APIURL, cfg.Token, cfg.ProjectID, resources)
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

func (r *Resolver) Resolve(ctx context.Context, baseURL, token, projectID string, resources []resourceregistry.Entry) ([]resolve.Resource, error) {
	if len(resources) == 0 {
		return []resolve.Resource{}, nil
	}

	defs := make([]resolvecache.Def, 0, len(resources))
	for _, entry := range resources {
		fragment, err := envFragment(entry.Type)
		if err != nil {
			return nil, err
		}
		defs = append(defs, resolvecache.Def{Name: entry.Name, Type: fragment})
	}
	defsHash := resolvecache.HashDefs(defs)
	account := resolvecache.AccountFingerprint(baseURL, token)

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

func (r *Resolver) callResolve(ctx context.Context, baseURL, token, projectID string, resources []resourceregistry.Entry) (map[string]string, time.Time, error) {
	entries := make([]resolveResourceEntry, 0, len(resources))
	for _, resource := range resources {
		fragment, err := envFragment(resource.Type)
		if err != nil {
			return nil, time.Time{}, err
		}
		entries = append(entries, resolveResourceEntry{Name: resource.Name, Type: fragment})
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

func resourcesFromEnv(resources []resourceregistry.Entry, env map[string]string) ([]resolve.Resource, error) {
	out := make([]resolve.Resource, 0, len(resources))
	for _, resource := range resources {
		fragment, err := envFragment(resource.Type)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("OCEL_RESOURCE_%s_%s", fragment, resource.Name)
		value, ok := env[key]
		if !ok {
			return nil, fmt.Errorf("resolve response missing env for resource %q", resource.Name)
		}
		out = append(out, resolve.Resource{Name: resource.Name, Type: resource.Type, Env: map[string]string{key: value}})
	}
	return out, nil
}

func envFragment(t linksv1.LinkType) (string, error) {
	if _, ok := naming.KindOf(t); !ok {
		return "", fmt.Errorf("resource has unsupported type %s", t)
	}
	return naming.EnvFragment(t), nil
}
