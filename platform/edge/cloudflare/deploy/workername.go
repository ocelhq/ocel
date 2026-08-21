package cloudflare

import (
	"fmt"
	"strings"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	workerNamespace = "ocel"
	fieldSeparator  = "--"
	wordSeparator   = "-"

	rootWorkerApp       = "root"
	productionWorkerEnv = "prod"
	previewWorkerEnv    = "preview"
)

func workerEnvFor(class edge.Class) (string, error) {
	switch class {
	case edge.ClassProduction:
		return productionWorkerEnv, nil
	case edge.ClassPreview:
		return previewWorkerEnv, nil
	default:
		return "", fmt.Errorf("stack workers: unknown class %q", class)
	}
}

func conventionWorkerNames(slug string, class edge.Class, apps []string) ([]string, error) {
	if slug == "" || class == "" {
		return nil, nil
	}
	env, err := workerEnvFor(class)
	if err != nil {
		return nil, err
	}
	names := []string{strings.Join([]string{workerNamespace, slug, env}, wordSeparator)}
	for _, app := range append([]string{rootWorkerApp}, apps...) {
		names = append(names,
			strings.Join([]string{workerNamespace, slug}, wordSeparator)+fieldSeparator+strings.Join([]string{env, app}, wordSeparator),
			strings.Join([]string{workerNamespace, slug, env, app}, fieldSeparator),
		)
	}
	return names, nil
}
