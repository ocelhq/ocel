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
	ValueOwnerLabel  = "ocel-value-owner"

	maxLabelValue = 63
)

func LabelValue(value string) (string, error) {
	sanitized := naming.Sanitize(value)
	if len(sanitized) <= maxLabelValue {
		return sanitized, nil
	}
	return naming.TaggedName{Readable: sanitized, Tag: naming.HashTag(sanitized)}.Fit(maxLabelValue)
}

func StackLabels(names Names, ref provider.StackRef) (map[string]string, error) {
	labels := map[string]string{}
	for key, value := range map[string]string{
		NamespaceLabel:   string(names.namespace),
		TierLabel:        string(ref.Tier),
		ProjectLabel:     ref.Project,
		EnvironmentLabel: ref.Name.Env,
	} {
		label, err := LabelValue(value)
		if err != nil {
			return nil, err
		}
		labels[key] = label
	}
	return labels, nil
}

func LabelsFor(names Names, ref provider.StackRef, kind, resource string) (map[string]string, error) {
	labels, err := StackLabels(names, ref)
	if err != nil {
		return nil, err
	}
	if labels[kind], err = LabelValue(resource); err != nil {
		return nil, err
	}
	return labels, nil
}
