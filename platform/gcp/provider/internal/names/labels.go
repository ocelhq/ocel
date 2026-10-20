package names

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	NamespaceLabel   = "ocel-namespace"
	TierLabel        = "ocel-tier"
	ProjectLabel     = "ocel-project"
	EnvironmentLabel = "ocel-environment"
	AppLabel         = "ocel-app"
	KVLabel          = "ocel-kv"
	BucketLabel      = "ocel-bucket"
	PostgresLabel    = "ocel-postgres"

	maxLabelValue = 63
)

func LabelValue(value string) string {
	sanitized := naming.Sanitize(value)
	if len(sanitized) <= maxLabelValue {
		return sanitized
	}
	return naming.TaggedName{Readable: sanitized, Tag: naming.HashTag(sanitized)}.Fit(maxLabelValue)
}

func StackLabels(names Names, ref provider.StackRef) map[string]string {
	return map[string]string{
		NamespaceLabel:   LabelValue(string(names.namespace)),
		TierLabel:        LabelValue(string(ref.Tier)),
		ProjectLabel:     LabelValue(ref.Project),
		EnvironmentLabel: LabelValue(ref.Name.Env),
	}
}

func LabelsFor(names Names, ref provider.StackRef, kind, resource string) map[string]string {
	labels := StackLabels(names, ref)
	labels[kind] = LabelValue(resource)
	return labels
}
