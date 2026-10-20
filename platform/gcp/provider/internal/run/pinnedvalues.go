package run

import (
	"maps"
	"slices"

	run "google.golang.org/api/run/v2"
)

type SecretVersion struct {
	Secret  string
	Version string
}

type PinnedValues map[string]SecretVersion

func environmentOf(values map[string]string, pinned PinnedValues) []*run.GoogleCloudRunV2EnvVar {
	entries := make([]*run.GoogleCloudRunV2EnvVar, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		version, held := pinned[name]
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
