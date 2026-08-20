package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/progress/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func workerOutputName(app string) string {
	return naming.Join(naming.WordSeparator, app, string(naming.KindWorker))
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

func functionURLsByLogicalName(functions []*progressv1.FunctionOutput) map[string]string {
	urls := make(map[string]string)
	for _, fn := range functions {
		urls[fn.GetLogicalName()] = fn.GetUrl()
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

func DeclaredHostnames(manifest *deploymentsv1.Manifest, tier environmentv1.Tier) []string {
	key := domainClassKeyFor(tier)
	var hosts []string
	add := func(names []string) {
		for _, host := range names {
			if host != "" && !slices.Contains(hosts, host) {
				hosts = append(hosts, host)
			}
		}
	}
	add(manifest.GetDomains()[key].GetHostnames())
	for _, app := range manifestApps(manifest) {
		add(app.GetDomains()[key].GetHostnames())
	}
	return hosts
}

func domainClassKeyFor(tier environmentv1.Tier) string {
	if tier == environmentv1.Tier_TIER_PREVIEW {
		return "preview"
	}
	return "production"
}

func workerDomains(cfg Config, manifest *deploymentsv1.Manifest, apps []*deploymentsv1.ManifestApp) (map[string][]string, error) {
	if cfg.Tier != environmentv1.Tier_TIER_PRODUCTION &&
		cfg.Tier != environmentv1.Tier_TIER_PREVIEW {
		return nil, nil
	}
	domainClassKey := domainClassKeyFor(cfg.Tier)

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
	case cfg.Tier == environmentv1.Tier_TIER_PREVIEW:
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
