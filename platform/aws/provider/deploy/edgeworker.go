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
				Name:    name,
				Runtime: fn.GetRuntime(),
				Compute: string(providerkit.ComputeServerless),
			})
		}
	}
	return apps
}

const runtimeNext = providerkit.RuntimeNext

const appsDirName = "apps"

func appArtifactRoot(artifactRoot, app string) string {
	return filepath.Join(artifactRoot, appsDirName, app)
}

const (
	maxWorkerNameLen = 63
	workerNamespace  = "ocel"
	previewWorkerEnv = "preview"
	rootWorkerApp    = "root"
)

func projectWorkerStem(slug string) string {
	return naming.Join(naming.FieldSeparator, workerNamespace, slug) + naming.FieldSeparator
}

func workerScriptName(slug, env, app string) string {
	return naming.Fit(maxWorkerNameLen, naming.FieldSeparator,
		naming.Fixed(workerNamespace),
		naming.Fixed(slug),
		naming.Fixed(env),
		naming.Compressible(app),
	)
}

func rootWorkerName(slug, env string) string {
	return workerScriptName(slug, env, rootWorkerApp)
}

func previewWorkerName(slug string) string {
	return rootWorkerName(slug, previewWorkerEnv)
}

func previewWorkerStem(slug string) string {
	return naming.Join(naming.FieldSeparator, workerNamespace, slug, previewWorkerEnv)
}

func ProjectOwnsWorker(slug, script string) bool {
	if slug == "" || script == "" {
		return false
	}
	return strings.HasPrefix(script, projectWorkerStem(slug)) ||
		strings.HasPrefix(script, retiredProjectWorkerStem(slug))
}

func retiredProjectWorkerStem(slug string) string {
	return naming.Join(naming.WordSeparator, workerNamespace, slug) + naming.FieldSeparator
}
