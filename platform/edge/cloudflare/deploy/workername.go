package cloudflare

import (
	"fmt"

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

func workerScriptName(namespace, slug, env, app string) string {
	return naming.Fit(maxWorkerNameLength, naming.FieldSeparator,
		naming.Fixed(naming.NamespaceField(namespace)),
		naming.Fixed(slug),
		naming.Fixed(env),
		naming.Compressible(app),
	)
}

func entryWorkerName(namespace, slug string, tier environment.Tier) (string, error) {
	env, err := workerEnvFor(tier)
	if err != nil {
		return "", err
	}
	return workerScriptName(namespace, slug, env, rootWorkerApp), nil
}

func ProjectOwnsWorker(namespace, slug, script string) bool {
	return projectOwnsScript(namespace, slug)(script)
}
