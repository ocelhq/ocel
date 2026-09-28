package cloudflare

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	fieldSeparator = "--"
	wordSeparator  = "-"

	rootWorkerApp       = "root"
	productionWorkerEnv = "prod"
	previewWorkerEnv    = "preview"
)

func workerEnvFor(tier environment.Tier) (string, error) {
	switch tier {
	case environment.TierProduction:
		return productionWorkerEnv, nil
	case environment.TierPreview:
		return previewWorkerEnv, nil
	default:
		return "", fmt.Errorf("stack workers: unknown tier %q", tier)
	}
}

func conventionWorkerNames(namespace, slug string, tier environment.Tier, apps []string) ([]string, error) {
	if namespace == "" || slug == "" || tier == "" {
		return nil, nil
	}
	env, err := workerEnvFor(tier)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, app := range append([]string{rootWorkerApp}, apps...) {
		names = append(names, strings.Join([]string{naming.NamespaceField(namespace), slug, env, app}, fieldSeparator))
	}
	return names, nil
}
