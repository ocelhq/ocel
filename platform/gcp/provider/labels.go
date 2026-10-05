package gcp

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	namespaceLabel   = "ocel-namespace"
	tierLabel        = "ocel-tier"
	projectLabel     = "ocel-project"
	environmentLabel = "ocel-environment"
	kvLabel          = "ocel-kv"
	bucketLabel      = "ocel-bucket"

	maxLabelValue = 63
)

func labelValue(value string) string {
	return naming.Fit(maxLabelValue, naming.WordSeparator, naming.Compressible(value))
}

func stackLabels(names Names, ref provider.StackRef) map[string]string {
	return map[string]string{
		namespaceLabel:   labelValue(string(names.namespace)),
		tierLabel:        labelValue(string(ref.Tier)),
		projectLabel:     labelValue(ref.Project),
		environmentLabel: labelValue(ref.Name.Env),
	}
}

func labelsFor(names Names, ref provider.StackRef, kind, resource string) map[string]string {
	labels := stackLabels(names, ref)
	labels[kind] = labelValue(resource)
	return labels
}
