package deploy

import (
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

const appsDirName = "apps"

func appArtifactRoot(artifactRoot, app string) string {
	return filepath.Join(artifactRoot, appsDirName, app)
}

const (
	maxWorkerNameLen = 63
	previewWorkerEnv = "preview"
	rootWorkerApp    = "root"
)

func projectWorkerStem(namespace, slug string) string {
	return naming.Join(naming.FieldSeparator, naming.NamespaceField(namespace), slug) + naming.FieldSeparator
}

func workerScriptName(namespace, slug, env, app string) string {
	return naming.Fit(maxWorkerNameLen, naming.FieldSeparator,
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
	if namespace == "" || slug == "" || script == "" {
		return false
	}
	return strings.HasPrefix(script, projectWorkerStem(namespace, slug))
}
