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

	maxWorkerNameLength = 63
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

func workerScriptName(namespace, slug, env, app string) string {
	return naming.Fit(maxWorkerNameLength, naming.FieldSeparator,
		naming.Fixed(naming.NamespaceField(namespace)),
		naming.Fixed(slug),
		naming.Fixed(env),
		naming.Compressible(app),
	)
}

func rootWorkerName(namespace, slug, env string) string {
	return workerScriptName(namespace, slug, env, rootWorkerApp)
}

func previewWorkerName(namespace, slug string) string {
	return rootWorkerName(namespace, slug, previewWorkerEnv)
}

func previewWorkerStem(namespace, slug string) string {
	return naming.Join(naming.FieldSeparator, naming.NamespaceField(namespace), slug, previewWorkerEnv)
}

func ProjectOwnsWorker(namespace, slug, script string) bool {
	return projectOwnsScript(namespace, slug)(script)
}
