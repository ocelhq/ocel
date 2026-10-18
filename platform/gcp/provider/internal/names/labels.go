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
	return naming.Fit(maxLabelValue, naming.WordSeparator, naming.Compressible(value))
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
