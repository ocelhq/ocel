package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func workerOutputName(app string) string {
	return naming.Join(naming.WordSeparator, app, string(naming.KindWorker))
}

func deployEdgeWorker(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, outputs []*deploymentsv1.ResourceOutput, progress func(string)) ([]*deploymentsv1.ResourceOutput, error) {
	warnOrphanedWorker(ctx, cfg, progress)

	apps := workerApps(cfg.ArtifactRoot, manifest)
	if len(apps) == 0 {
		return nil, nil
	}
	if cfg.Edge == nil {
		return nil, fmt.Errorf("project has an edge-served %s app but no edge is configured", apps[0].GetFramework())
	}
	domains, err := workerDomains(cfg, manifest, apps)
	if err != nil {
		return nil, err
	}
	bundles, err := edge.LoadBundleManifest()
	if err != nil {
		return nil, err
	}
	urlByLogical := functionURLsByLogicalName(outputs)

	var workerOutputs []*deploymentsv1.ResourceOutput
	bundlePath, err := bundles.Path(cfg.Edge.Kind())
	if err != nil {
		return nil, err
	}

	for _, app := range apps {
		name := app.GetName()
		worker, err := cfg.Edge.AssembleApp(
			edge.WorkerSource{
				ArtifactRoot: appArtifactRoot(cfg.ArtifactRoot, name),
				BundlePath:   bundlePath,
				Routes:       appRoutes(manifest.GetFunctions(), app),
			},
			&deployResolver{
				cfg:      cfg,
				manifest: manifest,
				app:      name,
				urls:     appFunctionURLsByRoute(manifest.GetFunctions(), name, urlByLogical),
			},
		)
		if err != nil {
			return nil, err
		}

		if progress != nil {
			progress(fmt.Sprintf("Deploying %s to the edge", name))
		}
		result, err := cfg.Edge.DeployApp(ctx, edge.AppDeployment{
			Name:    workerScriptName(cfg.Slug, cfg.Env, name),
			Domains: domains[name],
			Worker:  worker,
			Values:  cfg.EdgeValues,
			Warn:    progress,
		})
		if err != nil {
			return nil, fmt.Errorf("deploy edge worker for %s: %w", name, err)
		}
		workerOutputs = append(workerOutputs, collectFunctionOutput(workerOutputName(name), result.URL))
	}
	return workerOutputs, nil
}

func manifestApps(manifest *deploymentsv1.Manifest) []*deploymentsv1.ManifestApp {
	if apps := manifest.GetApps(); len(apps) > 0 {
		return apps
	}
	var apps []*deploymentsv1.ManifestApp
	seen := map[string]bool{}
	for _, fn := range manifest.GetFunctions() {
		if name := fn.GetApp(); !seen[name] {
			seen[name] = true
			apps = append(apps, &deploymentsv1.ManifestApp{Name: name, Framework: fn.GetFramework()})
		}
	}
	return apps
}

// An app is served from the edge when its build emitted a serve descriptor —
// a fact about the artifact, not a framework this package has to know by name.
func workerApps(artifactRoot string, manifest *deploymentsv1.Manifest) []*deploymentsv1.ManifestApp {
	var apps []*deploymentsv1.ManifestApp
	for _, app := range manifestApps(manifest) {
		if isEdgeServed(artifactRoot, app.GetName()) && len(appRoutes(manifest.GetFunctions(), app)) > 0 {
			apps = append(apps, app)
		}
	}
	return apps
}

func isEdgeServed(artifactRoot, app string) bool {
	_, err := os.Stat(filepath.Join(appArtifactRoot(artifactRoot, app), edge.ServeDescriptorFile))
	return err == nil
}

func readServeDescriptor(artifactRoot, app string) (edge.ServeDescriptor, bool, error) {
	raw, err := os.ReadFile(filepath.Join(appArtifactRoot(artifactRoot, app), edge.ServeDescriptorFile))
	if errors.Is(err, fs.ErrNotExist) {
		return edge.ServeDescriptor{}, false, nil
	}
	if err != nil {
		return edge.ServeDescriptor{}, false, fmt.Errorf("read serve descriptor for %s: %w", app, err)
	}
	var desc edge.ServeDescriptor
	if err := json.Unmarshal(raw, &desc); err != nil {
		return edge.ServeDescriptor{}, false, fmt.Errorf("parse serve descriptor for %s: %w", app, err)
	}
	return desc, true, nil
}

func appRoutes(functions []*deploymentsv1.ManifestFunction, app *deploymentsv1.ManifestApp) []string {
	var routes []string
	for _, fn := range functions {
		if fn.GetApp() == app.GetName() && fn.GetFramework() == app.GetFramework() {
			routes = append(routes, routeID(fn))
		}
	}
	return routes
}

const frameworkNext = "next"

const appsDirName = "apps"

func appArtifactRoot(artifactRoot, app string) string {
	return filepath.Join(artifactRoot, appsDirName, app)
}

func routeID(fn *deploymentsv1.ManifestFunction) string {
	if id := fn.GetRouteId(); id != "" {
		return id
	}
	return fn.GetLogicalName()
}

func functionURLsByLogicalName(outputs []*deploymentsv1.ResourceOutput) map[string]string {
	urls := make(map[string]string)
	for _, o := range outputs {
		if fn := o.GetFunction(); fn != nil {
			urls[o.GetLogicalName()] = fn.GetUrl()
		}
	}
	return urls
}

func appFunctionURLsByRoute(functions []*deploymentsv1.ManifestFunction, app string, urlByLogical map[string]string) map[string]string {
	result := make(map[string]string)
	for _, fn := range functions {
		if fn.GetApp() != app {
			continue
		}
		if url := urlByLogical[fn.GetLogicalName()]; url != "" {
			result[routeID(fn)] = url
		}
	}
	return result
}

type deployResolver struct {
	cfg      Config
	manifest *deploymentsv1.Manifest
	app      string
	urls     map[string]string
}

func (d *deployResolver) FunctionURL(routeID string) (string, error) {
	url := d.urls[routeID]
	if url == "" {
		return "", fmt.Errorf("no Function URL was realized for route %q; the worker could not serve it", routeID)
	}
	return url, nil
}

func (d *deployResolver) EdgeCredentials() (edge.Credentials, bool) {
	if d.cfg.EdgeAccessKeyID == "" || d.cfg.EdgeSecretKey == "" {
		return edge.Credentials{}, false
	}
	return edge.Credentials{
		AccessKeyID: d.cfg.EdgeAccessKeyID,
		SecretKey:   d.cfg.EdgeSecretKey,
	}, true
}

func domainClassKeyFor(class deploymentsv1.Environment_Class) string {
	if class == deploymentsv1.Environment_CLASS_PREVIEW {
		return "preview"
	}
	return "production"
}

func workerDomains(cfg Config, manifest *deploymentsv1.Manifest, apps []*deploymentsv1.ManifestApp) (map[string][]string, error) {
	if cfg.Class != deploymentsv1.Environment_CLASS_PRODUCTION &&
		cfg.Class != deploymentsv1.Environment_CLASS_PREVIEW {
		return nil, nil
	}
	domainClassKey := domainClassKeyFor(cfg.Class)

	domains := map[string][]string{}
	var undeclared []string
	for _, app := range apps {
		if d := app.GetDomains()[domainClassKey].GetHostnames(); len(d) > 0 {
			domains[app.GetName()] = d
			continue
		}
		undeclared = append(undeclared, app.GetName())
	}

	project := manifest.GetDomains()[domainClassKey].GetHostnames()
	switch {
	case len(project) == 0 || len(undeclared) == 0:
		return domains, nil
	case cfg.Class == deploymentsv1.Environment_CLASS_PREVIEW:
		for _, app := range undeclared {
			domains[app] = project
		}
		return domains, nil
	case len(apps) == 1:
		domains[undeclared[0]] = project
		return domains, nil
	case len(undeclared) == len(apps):
		return nil, fmt.Errorf("the project-level domains %s are ambiguous: apps %s each run their own edge worker and none declares a domain of its own — give each app its own domain instead", quotedList(project), quotedList(undeclared))
	default:
		return domains, nil
	}
}

func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	if len(quoted) < 2 {
		return strings.Join(quoted, "")
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
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

func retiredPreviewWorkerStem(slug string) string {
	return retiredProjectWorkerStem(slug) + previewWorkerEnv
}

func retiredWorkerNames(slug, env string, apps []string) []string {
	names := []string{legacyWorkerName(slug, env)}
	for _, app := range append([]string{rootWorkerApp}, apps...) {
		names = append(names, retiredProjectWorkerStem(slug)+naming.Join(naming.WordSeparator, env, app))
	}
	return names
}

func legacyWorkerName(slug, env string) string {
	return naming.Join(naming.WordSeparator, workerNamespace, slug, env)
}

func warnOrphanedWorker(ctx context.Context, cfg Config, progress func(string)) {
	if cfg.Edge == nil || progress == nil {
		return
	}
	finder, ok := cfg.Edge.(edge.AppFinder)
	if !ok {
		return
	}
	name := legacyWorkerName(cfg.Slug, cfg.Env)
	if found, err := finder.FindApp(ctx, name); err != nil || !found {
		return
	}
	progress(fmt.Sprintf("Warning: an edge worker remains at %q, the name this project deployed under before workers were named per app. Deploys no longer update it; delete it once nothing points at it.", name))
}
