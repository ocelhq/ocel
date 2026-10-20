package run

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/api/googleapi"
	runv1 "google.golang.org/api/run/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type SecretVersion struct {
	Secret  string
	Version string
}

type PinnedValues struct {
	Owner    string
	Versions map[string]SecretVersion
}

func (p PinnedValues) hasAnyOf(env map[string]string) bool {
	for name := range p.Versions {
		if _, set := env[name]; set {
			return true
		}
	}
	return false
}

func environmentOf(values map[string]string, pinned PinnedValues) []*run.GoogleCloudRunV2EnvVar {
	entries := make([]*run.GoogleCloudRunV2EnvVar, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		version, held := pinned.Versions[name]
		if !held {
			entries = append(entries, &run.GoogleCloudRunV2EnvVar{Name: name, Value: values[name]})
			continue
		}
		entries = append(entries, &run.GoogleCloudRunV2EnvVar{Name: name, ValueSource: &run.GoogleCloudRunV2EnvVarSource{
			SecretKeyRef: &run.GoogleCloudRunV2SecretKeySelector{Secret: version.Secret, Version: version.Version},
		}})
	}
	return entries
}

func (p *Services) ListPinnedVersions(ctx context.Context, owner string) (map[string][]string, error) {
	clients, err := p.OpenClients(ctx)
	if err != nil {
		return nil, err
	}
	revisions, err := clients.RunV1()
	if err != nil {
		return nil, err
	}
	pinned := map[string][]string{}
	for token := ""; ; {
		listed, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*runv1.ListRevisionsResponse, error) {
			return revisions.Namespaces.Revisions.List("namespaces/" + clients.Project()).
				LabelSelector(names.ValueOwnerLabel + "=" + owner).Continue(token).Context(ctx).Do(call...)
		})
		if err != nil {
			return nil, fmt.Errorf("list the revisions that pin values of %s: %w", owner, err)
		}
		for _, revision := range listed.Items {
			for _, version := range pinnedBy(revision) {
				if !slices.Contains(pinned[version.Secret], version.Version) {
					pinned[version.Secret] = append(pinned[version.Secret], version.Version)
				}
			}
		}
		if listed.Metadata == nil || listed.Metadata.Continue == "" {
			return pinned, nil
		}
		token = listed.Metadata.Continue
	}
}

func pinnedBy(revision *runv1.Revision) []SecretVersion {
	if revision.Spec == nil {
		return nil
	}
	var pinned []SecretVersion
	for _, container := range revision.Spec.Containers {
		for _, entry := range container.Env {
			if entry.ValueFrom != nil && entry.ValueFrom.SecretKeyRef != nil {
				pinned = append(pinned, SecretVersion{Secret: RevisionName(entry.ValueFrom.SecretKeyRef.Name), Version: entry.ValueFrom.SecretKeyRef.Key})
			}
		}
	}
	return pinned
}
