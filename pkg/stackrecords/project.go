package stackrecords

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
)

type Project struct {
	Parts     []string         `json:"parts,omitempty"`
	Edge      *ProjectEdge     `json:"edge,omitempty"`
	Registry  *ProjectRegistry `json:"registry,omitempty"`
	UpdatedAt int64            `json:"updated_at,omitempty"`
}

type ProjectEdge struct {
	Kind          edge.Kind       `json:"kind"`
	DNSKind       string          `json:"dnsKind,omitempty"`
	DNSZone       string          `json:"dnsZone,omitempty"`
	AllowDegraded []string        `json:"allowDegraded,omitempty"`
	Options       json.RawMessage `json:"options,omitempty"`
}

type ProjectRegistry struct {
	Server    string `json:"server"`
	Namespace string `json:"namespace,omitempty"`
	Username  string `json:"username,omitempty"`
}

type NamedProject struct {
	Slug string
	Project
}

func ListProjects(ctx context.Context, store records.Store, tier environment.Tier) ([]NamedProject, error) {
	entries, err := store.List(ctx, ProjectsPartition(tier))
	if err != nil {
		return nil, err
	}
	var projects []NamedProject
	for _, entry := range entries {
		if len(entry.Key.Path) != 1 {
			continue
		}
		project, err := DecodeProject(entry)
		if err != nil {
			return nil, err
		}
		projects = append(projects, NamedProject{Slug: entry.Key.Path[0], Project: project})
	}
	slices.SortFunc(projects, func(a, b NamedProject) int { return strings.Compare(a.Slug, b.Slug) })
	return projects, nil
}

func ReadProject(ctx context.Context, store records.Store, tier environment.Tier, slug string) (Project, bool, error) {
	recorded, err := records.ReadOrEmpty(ctx, store, ProjectKey(tier, slug))
	if err != nil {
		return Project{}, false, err
	}
	if recorded.Revision == "" {
		return Project{}, false, nil
	}
	project, err := DecodeProject(recorded)
	return project, err == nil, err
}

func DecodeProject(entry records.Entry) (Project, error) {
	var project Project
	if len(entry.Value) == 0 {
		return project, nil
	}
	if err := json.Unmarshal(entry.Value, &project); err != nil {
		return Project{}, fmt.Errorf("read %s: %w", entry.Key, err)
	}
	return project, nil
}
