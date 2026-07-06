// Package provision fetches project identity and resolves declared
// resources to live connections.
//
// Both entry points are stubbed until the real Ocel API exists, but their
// signatures are final: the rest of the CLI (see internal/cli.devCmd)
// depends only on these shapes, so wiring in the real implementation later
// requires no caller changes.
package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

// FetchProjectConfig fetches the org/project/user identity and project-level
// environment for projectID. Stubbed: real implementation will call the
// Ocel API with apiURL and token.
func FetchProjectConfig(_ context.Context, _, _, projectID string) (ProjectConfig, error) {
	return ProjectConfig{
		OrgID:     "org_stub",
		ProjectID: projectID,
		UserID:    "user_stub",
		EnvVars:   map[string]string{},
	}, nil
}

// Provision resolves each declared resource to a live connection. Stubbed:
// real implementation will check out an instance from a warm pool via the
// Ocel API; the CLI never provisions directly.
func Provision(_ context.Context, _ ProjectConfig, resources []manifest.Entry) ([]ProvisionedResource, error) {
	out := make([]ProvisionedResource, 0, len(resources))
	for _, r := range resources {
		typeName, err := resourceTypeName(r.Type)
		if err != nil {
			return nil, err
		}

		connectionString := fmt.Sprintf("postgres://stub:stub@localhost:5432/%s", r.Name)
		envValue, err := json.Marshal(struct {
			ConnectionString string `json:"connectionString"`
		}{ConnectionString: connectionString})
		if err != nil {
			return nil, fmt.Errorf("encode env for resource %q: %w", r.Name, err)
		}

		key := fmt.Sprintf("OCEL_RESOURCE_%s_%s", typeName, r.Name)
		out = append(out, ProvisionedResource{
			Name: r.Name,
			Type: r.Type,
			Env:  map[string]string{key: string(envValue)},
		})
	}
	return out, nil
}

// resourceTypeName renders a ResourceType as it appears in
// OCEL_RESOURCE_<TYPE>_<name>, matching the SDK's getConfig (see
// packages/ocel/src/utils/get-config.ts).
func resourceTypeName(t resourcesv1.ResourceType) (string, error) {
	if t == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return "", fmt.Errorf("resource has unspecified type")
	}
	return strings.TrimPrefix(t.String(), "RESOURCE_TYPE_"), nil
}
