// Package manifestbuilder lowers a project's collected resource
// declarations into the versioned provider.v1 Manifest a provider consumes.
// Build is a pure function: given the same declarations (in any order), it
// always returns byte-identical output, so a manifest's logical names are a
// stable identity providers and later deploys can rely on.
package manifestbuilder

import (
	"fmt"
	"sort"
	"strings"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// SchemaVersion is the manifest schema version Build stamps onto every
// manifest it produces.
const SchemaVersion = "provider.v1"

// Declaration is a single collected resource declaration: the pure input to
// Build. Source is a caller-supplied, human-readable location (e.g. a file
// path) identifying where the declaration came from, surfaced in duplicate
// errors — Build treats it as an opaque string and never parses it.
type Declaration struct {
	Type     resourcesv1.ResourceType
	ID       string
	Postgres *resourcesv1.PostgresConfig
	Bucket   *resourcesv1.BucketConfig
	Source   string
}

// Function is a single collected function unit: the pure input to Build for
// Manifest.functions. Name is the app name, normalized into the function's
// logical_name by the same rule as a resource's.
type Function struct {
	Name         string
	Runtime      string
	Handler      string
	ArtifactPath string
	Framework    string
	// RouteID is the framework-native route identity a routing layer dispatches
	// to (e.g. Next's "/api/documents"), carried verbatim into
	// ManifestFunction.route_id — unlike Name, it is never normalized. Empty for
	// functions whose framework has no routing layer.
	RouteID string
}

// DuplicateError is returned by Build when two declarations resolve to the
// same type+id. It names both offending declarations and their source
// locations, rather than a bare "duplicate key".
type DuplicateError struct {
	TypeToken    string
	ID           string
	FirstSource  string
	SecondSource string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf(
		"manifestbuilder: duplicate resource declaration for type=%s id=%q: declared at %s and %s",
		e.TypeToken, e.ID, sourceOrUnknown(e.FirstSource), sourceOrUnknown(e.SecondSource),
	)
}

func sourceOrUnknown(source string) string {
	if source == "" {
		return "<unknown source>"
	}
	return source
}

// typeTokens maps a resources.v1.ResourceType to its canonical lowercase
// token, the <type> half of a logical name (e.g. "postgres"). This is the
// single place a new resource type's token is defined.
var typeTokens = map[resourcesv1.ResourceType]string{
	resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES: "postgres",
	resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:   "bucket",
}

func typeToken(t resourcesv1.ResourceType) (string, error) {
	token, ok := typeTokens[t]
	if !ok {
		return "", fmt.Errorf("manifestbuilder: unsupported resource type %v", t)
	}
	return token, nil
}

// normalizeLogicalName applies the single deterministic rule that maps a
// composed <type>_<id> string to the manifest's logical-name charset:
// lowercase ASCII letters are lowercased, and any character outside
// [a-z0-9_] (including the already-lowercase ones) is replaced with '_'.
// Fixed by the golden test in manifestbuilder_test.go.
func normalizeLogicalName(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// Build lowers declarations into a Manifest for projectID. Output is
// deterministic: entries are emitted sorted by logical_name (not input
// order), so reordering declarations or adding a new one never changes an
// existing entry's logical name. Two declarations sharing the same
// (type, id) are a hard error naming both declarations and their source
// locations.
func Build(projectID string, domains map[string]string, declarations []Declaration, functions []Function) (*deploymentsv1.Manifest, error) {
	type identity struct {
		typ resourcesv1.ResourceType
		id  string
	}
	seen := make(map[identity]Declaration, len(declarations))

	resources := make([]*deploymentsv1.ManifestResource, 0, len(declarations))
	for _, d := range declarations {
		if d.ID == "" {
			return nil, fmt.Errorf("manifestbuilder: declaration has empty resource id")
		}

		token, err := typeToken(d.Type)
		if err != nil {
			return nil, err
		}

		id := identity{d.Type, d.ID}
		if prior, ok := seen[id]; ok {
			return nil, &DuplicateError{
				TypeToken:    token,
				ID:           d.ID,
				FirstSource:  prior.Source,
				SecondSource: d.Source,
			}
		}
		seen[id] = d

		resource := &deploymentsv1.ManifestResource{
			LogicalName: normalizeLogicalName(token + "_" + d.ID),
			Resource: &resourcesv1.ResourceIdentifier{
				Type: d.Type,
				Name: d.ID,
			},
		}
		if d.Postgres != nil {
			resource.Config = &deploymentsv1.ManifestResource_Postgres{Postgres: d.Postgres}
		}
		if d.Bucket != nil {
			resource.Config = &deploymentsv1.ManifestResource_Bucket{Bucket: d.Bucket}
		}
		resources = append(resources, resource)
	}

	sort.Slice(resources, func(i, j int) bool {
		return resources[i].LogicalName < resources[j].LogicalName
	})

	manifestFunctions := make([]*deploymentsv1.ManifestFunction, 0, len(functions))
	for _, f := range functions {
		manifestFunctions = append(manifestFunctions, &deploymentsv1.ManifestFunction{
			LogicalName:  normalizeLogicalName(f.Name),
			Runtime:      f.Runtime,
			Handler:      f.Handler,
			ArtifactPath: f.ArtifactPath,
			Framework:    f.Framework,
			RouteId:      f.RouteID,
		})
	}
	sort.Slice(manifestFunctions, func(i, j int) bool {
		return manifestFunctions[i].LogicalName < manifestFunctions[j].LogicalName
	})

	return &deploymentsv1.Manifest{
		SchemaVersion: SchemaVersion,
		ProjectId:     projectID,
		Resources:     resources,
		Functions:     manifestFunctions,
		Domains:       domains,
	}, nil
}
