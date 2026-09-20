package cloudflare

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	fieldSeparator = "--"
	wordSeparator  = "-"

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

func conventionWorkerNames(namespace, slug string, class edge.Class, apps []string) ([]string, error) {
	if namespace == "" || slug == "" || class == "" {
		return nil, nil
	}
	env, err := workerEnvFor(class)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, app := range append([]string{rootWorkerApp}, apps...) {
		names = append(names, strings.Join([]string{naming.NamespaceField(namespace), slug, env, app}, fieldSeparator))
	}
	return names, nil
}
