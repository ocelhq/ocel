package deploy

import (
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func workerOutputName(app string) string {
	return naming.Join(naming.WordSeparator, app, string(naming.KindWorker))
}

func manifestApps(manifest *contractv1.Manifest) []*contractv1.ManifestApp {
	if apps := manifest.GetApps(); len(apps) > 0 {
		return apps
	}
	var apps []*contractv1.ManifestApp
	seen := map[string]bool{}
	for _, fn := range manifest.GetFunctions() {
		if name := fn.GetApp(); !seen[name] {
			seen[name] = true
			apps = append(apps, &contractv1.ManifestApp{
				Name:      name,
				Framework: fn.GetFramework(),
				Compute:   string(providerkit.ComputeServerless),
			})
		}
	}
	return apps
}

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
