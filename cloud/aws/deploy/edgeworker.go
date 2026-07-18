package deploy

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/edge"
	"github.com/ocelhq/ocel/cloud/edge/framework"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// frameworkNext marks a ManifestFunction whose routes are fronted by the
// Next.js edge worker. It matches the value the adapter writes into each
// function's config.json.
const frameworkNext = string(edge.FrameworkNext)

// workerOutputName is the logical name one app's deployed worker URL is
// reported under in the stack outputs. It is derived from the app so every app
// in a project gets its own.
func workerOutputName(app string) string {
	return sanitizeWorkerName(app) + "-worker"
}

// deployEdgeWorker creates or updates the edge worker fronting each of this
// project's apps. The provider decides which apps are deployed, where, and
// under what names; each framework's registry entry decides what its worker
// contains, pulling the deploy values it needs through a resolver. An app whose
// framework registers no worker deploys nothing here and is served from its own
// Function URL.
func deployEdgeWorker(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, outputs []*deploymentsv1.ResourceOutput, progress func(string)) ([]*deploymentsv1.ResourceOutput, error) {
	warnOrphanedWorker(ctx, cfg, progress)

	apps := workerApps(manifest)
	if len(apps) == 0 {
		return nil, nil
	}
	if cfg.Edge == nil {
		return nil, fmt.Errorf("project has a %s app but no edge is configured", apps[0].GetFramework())
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
	for _, app := range apps {
		fw := edge.Framework(app.GetFramework())
		assemble, err := framework.WorkerFor(fw, cfg.Edge.Kind())
		if err != nil {
			return nil, err
		}
		bundlePath, err := bundles.Path(fw, cfg.Edge.Kind())
		if err != nil {
			return nil, err
		}

		name := app.GetName()
		worker, err := assemble(
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
			Name:   workerScriptName(cfg.StackName, name),
			Domain: domains[name],
			Worker: worker,
			Values: cfg.EdgeValues,
		})
		if err != nil {
			return nil, fmt.Errorf("deploy edge worker for %s: %w", name, err)
		}
		workerOutputs = append(workerOutputs, collectFunctionOutput(workerOutputName(name), result.URL))
	}
	return workerOutputs, nil
}

// manifestApps is the project's apps in manifest order. A manifest predating
// the app list yields one app per distinct app name its functions carry, so
// every path below sees a single shape.
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

// workerApps are the apps fronted by an edge worker, in manifest order. An app
// whose framework wants a worker but which emitted no functions — a Next.js
// static export, say — is not one of them: it produced no build output for a
// worker to route to, so it deploys as its static assets alone.
func workerApps(manifest *deploymentsv1.Manifest) []*deploymentsv1.ManifestApp {
	var apps []*deploymentsv1.ManifestApp
	for _, app := range manifestApps(manifest) {
		if framework.NeedsWorker(edge.Framework(app.GetFramework())) && len(appRoutes(manifest.GetFunctions(), app)) > 0 {
			apps = append(apps, app)
		}
	}
	return apps
}

// appRoutes are the framework-native route ids one app's worker serves, in
// manifest order: its own functions, and only those its framework fronts.
func appRoutes(functions []*deploymentsv1.ManifestFunction, app *deploymentsv1.ManifestApp) []string {
	var routes []string
	for _, fn := range functions {
		if fn.GetApp() == app.GetName() && fn.GetFramework() == app.GetFramework() {
			routes = append(routes, routeID(fn))
		}
	}
	return routes
}

// appsDirName mirrors the builder's per-app namespacing of the build output.
// Each app owns <ArtifactRoot>/apps/<app>, holding its functions, static
// assets, cache entries and routing manifest.
//
// This name is a cross-process, cross-language contract with no single home:
// packages/ocel/src/builder/layout.ts (APPS_DIR) writes the layout,
// cli/internal/appbuilder (appsDirName) discovers functions in it, and this
// package reads each app's artifacts from it. Change one, change all three.
const appsDirName = "apps"

// appArtifactRoot is the subtree of the build output belonging to one app —
// what an edge assembly and the prerender upload read their inputs from.
func appArtifactRoot(artifactRoot, app string) string {
	return filepath.Join(artifactRoot, appsDirName, app)
}

// routeID is the framework-native identity a worker dispatches to. The manifest
// carries it separately from the infra-safe logical_name that URL outputs are
// keyed by; a function predating route ids falls back to its logical name.
func routeID(fn *deploymentsv1.ManifestFunction) string {
	if id := fn.GetRouteId(); id != "" {
		return id
	}
	return fn.GetLogicalName()
}

// functionURLsByLogicalName indexes the realized Function URLs by the logical
// name each was reported under.
func functionURLsByLogicalName(outputs []*deploymentsv1.ResourceOutput) map[string]string {
	urls := make(map[string]string)
	for _, o := range outputs {
		if fn := o.GetFunction(); fn != nil {
			urls[o.GetLogicalName()] = fn.GetUrl()
		}
	}
	return urls
}

// appFunctionURLsByRoute joins the two namespaces a deploy names functions in:
// stack outputs are keyed by logical name, workers dispatch by route id. A
// route id is unique only within its app — two apps both serve "/" — so the
// join is scoped to one app.
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

// deployResolver answers a framework's worker assembly from this deploy's
// manifest and realized stack outputs, so the assembly receives exactly the
// values it asked for rather than every output in the deploy.
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

// CacheStore describes this app's ISR cache as an object store plus a tag clock.
// It reports not-configured — never an error — when the substrate predates edge
// credentials or the app has no prerendered content, so the worker degrades to
// forwarding prerender routes to the Lambda and the deploy still succeeds.
func (d *deployResolver) CacheStore() (edge.CacheStore, bool, error) {
	if d.cfg.EdgeAccessKeyID == "" || d.cfg.EdgeSecretKey == "" {
		return edge.CacheStore{}, false, nil
	}
	caches, err := appCaches(d.cfg, d.manifest)
	if err != nil {
		return edge.CacheStore{}, false, err
	}
	isr := caches[d.app]
	if isr == nil {
		return edge.CacheStore{}, false, nil
	}

	return edge.CacheStore{
		Bucket:        isr.Bucket,
		Prefix:        isr.Prefix,
		Region:        d.cfg.Region,
		TagTable:      isr.Table,
		TagTableIndex: bootstrap.StateTableIndexName,
		TagNamespace:  isr.tagNamespace(),
		Credentials: edge.Credentials{
			AccessKeyID: d.cfg.EdgeAccessKeyID,
			SecretKey:   d.cfg.EdgeSecretKey,
		},
	}, true, nil
}

// domainClassKey is the Manifest.domains key an environment class reads its
// custom hostname from. Only production has one: a preview is served on the
// edge's own vendor subdomain.
const domainClassKey = "production"

// workerDomains resolves the custom hostname each worker-backed app is served
// on, keyed by app name and absent where the app takes the edge's vendor
// subdomain. An app's own domain wins; the project-level domain applies only
// when the project has exactly one worker-backed app, because an apex domain
// cannot be split between two apps and Ocel does not guess which owns it.
func workerDomains(cfg Config, manifest *deploymentsv1.Manifest, apps []*deploymentsv1.ManifestApp) (map[string]string, error) {
	if cfg.Class != deploymentsv1.Environment_CLASS_PRODUCTION {
		return nil, nil
	}

	domains := map[string]string{}
	var undeclared []string
	for _, app := range apps {
		if d := app.GetDomains()[domainClassKey]; d != "" {
			domains[app.GetName()] = d
			continue
		}
		undeclared = append(undeclared, app.GetName())
	}

	project := manifest.GetDomains()[domainClassKey]
	switch {
	case project == "" || len(undeclared) == 0:
		return domains, nil
	case len(apps) == 1:
		domains[undeclared[0]] = project
		return domains, nil
	case len(undeclared) == len(apps):
		return nil, fmt.Errorf("the project-level domain %q is ambiguous: apps %s each run their own edge worker and none declares a domain of its own — give each app its own domain instead", project, quotedList(undeclared))
	default:
		return domains, nil
	}
}

// quotedList renders names for an error message: `"a"`, `"a" and "b"`, or
// `"a", "b" and "c"`.
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

// maxWorkerNameLen is the platform limit on an edge deployment name.
const maxWorkerNameLen = 63

// workerScriptName is the deterministic deployment identity of one app's
// worker: the project and environment, then the app. The app segment is what
// keeps two apps in one project apart, so the project-and-environment segment
// absorbs any clamping needed to fit the platform limit and the app segment is
// carried whole.
func workerScriptName(stackName, app string) string {
	appSegment := sanitizeWorkerName(app)
	budget := maxWorkerNameLen - len(appSegment) - 1
	if budget <= 0 {
		return appSegment
	}
	stackSegment := clamp(sanitizeWorkerName("ocel-"+stackName), budget)
	if stackSegment == "" {
		return appSegment
	}
	return stackSegment + "-" + appSegment
}

func clamp(name string, max int) string {
	if len(name) <= max {
		return name
	}
	return trimHyphens(name[:max])
}

// legacyWorkerName is the unqualified name a project's single worker was
// deployed under before script names were qualified by app.
func legacyWorkerName(stackName string) string {
	return sanitizeWorkerName("ocel-" + stackName)
}

// warnOrphanedWorker reports a worker still living at the previous unqualified
// name. Deploys no longer address it, but it keeps serving, so it is named
// rather than left behind silently — and never deleted, because only the user
// knows whether anything still points at it. An edge that cannot answer, or
// fails to, simply produces no warning.
func warnOrphanedWorker(ctx context.Context, cfg Config, progress func(string)) {
	if cfg.Edge == nil || progress == nil {
		return
	}
	finder, ok := cfg.Edge.(edge.AppFinder)
	if !ok {
		return
	}
	name := legacyWorkerName(cfg.StackName)
	if found, err := finder.FindApp(ctx, name); err != nil || !found {
		return
	}
	progress(fmt.Sprintf("Warning: an edge worker remains at %q, the name this project deployed under before workers were named per app. Deploys no longer update it; delete it once nothing points at it.", name))
}

// sanitizeWorkerName lowers an arbitrary identity into an edge deployment
// name: lowercase, every character outside [a-z0-9] replaced with '-',
// leading/trailing hyphens trimmed, and clamped to the platform limit. The
// result is deterministic so redeploys of the same project+env update the
// script in place.
func sanitizeWorkerName(s string) string {
	buf := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			buf = append(buf, byte(r))
		case r >= 'A' && r <= 'Z':
			buf = append(buf, byte(r-'A'+'a'))
		default:
			// Collapse any run of out-of-charset characters into a single hyphen.
			if len(buf) > 0 && buf[len(buf)-1] != '-' {
				buf = append(buf, '-')
			}
		}
	}
	name := clamp(trimHyphens(string(buf)), maxWorkerNameLen)
	if name == "" {
		return "ocel-worker"
	}
	return name
}

func trimHyphens(s string) string {
	for len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == '-' {
		s = s[:len(s)-1]
	}
	return s
}
