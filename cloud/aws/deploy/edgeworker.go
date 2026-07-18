package deploy

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// frameworkNext marks a ManifestFunction whose routes are fronted by the
// Next.js edge worker. It matches the value the adapter writes into each
// function's config.json.
const frameworkNext = string(edge.FrameworkNext)

// edgeWorkerOutputName is the logical name the deployed worker's public URL is
// reported under in the stack outputs.
const edgeWorkerOutputName = "next-worker"

// deployEdgeWorker creates or updates the edge worker fronting this project's
// app. The provider decides which app is deployed, where, and under what name;
// the framework's registry entry decides what the worker contains, pulling the
// deploy values it needs through a resolver. A framework registering no worker
// deploys nothing here and is served from its own Function URL.
func deployEdgeWorker(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, outputs []*deploymentsv1.ResourceOutput, progress func(string)) ([]*deploymentsv1.ResourceOutput, error) {
	framework, app, routes := appRoutes(manifest.GetFunctions())
	if !edge.NeedsWorker(framework) {
		return nil, nil
	}
	if cfg.Edge == nil {
		return nil, fmt.Errorf("project has a %s app but no edge is configured", framework)
	}

	assemble, err := edge.WorkerFor(framework, cfg.Edge.Kind())
	if err != nil {
		return nil, err
	}
	bundles, err := edge.LoadBundleManifest()
	if err != nil {
		return nil, err
	}
	bundlePath, err := bundles.Path(framework, cfg.Edge.Kind())
	if err != nil {
		return nil, err
	}

	worker, err := assemble(
		edge.WorkerSource{ArtifactRoot: appArtifactRoot(cfg.ArtifactRoot, app), BundlePath: bundlePath, Routes: routes},
		&deployResolver{cfg: cfg, manifest: manifest, urls: functionURLsByRoute(manifest.GetFunctions(), outputs)},
	)
	if err != nil {
		return nil, err
	}

	if progress != nil {
		progress("Deploying app worker to the edge")
	}
	result, err := cfg.Edge.DeployApp(ctx, edge.AppDeployment{
		Name:   sanitizeWorkerName("ocel-" + cfg.StackName),
		Domain: productionDomain(cfg, manifest),
		Worker: worker,
	})
	if err != nil {
		return nil, fmt.Errorf("deploy edge worker: %w", err)
	}

	return []*deploymentsv1.ResourceOutput{
		collectFunctionOutput(edgeWorkerOutputName, result.URL),
	}, nil
}

// appRoutes reports the framework fronting this project's app, that app's name,
// and the framework-native route ids it serves, in manifest order. The
// single-app assumption lives here: every function declaring the first
// framework seen is taken to belong to one app.
func appRoutes(functions []*deploymentsv1.ManifestFunction) (edge.Framework, string, []string) {
	var framework edge.Framework
	var app string
	var routes []string
	for _, fn := range functions {
		declared := edge.Framework(fn.GetFramework())
		if declared == "" {
			continue
		}
		if framework == "" {
			framework, app = declared, fn.GetApp()
		}
		if declared == framework {
			routes = append(routes, routeID(fn))
		}
	}
	return framework, app, routes
}

// appsDirName mirrors the builder's per-app namespacing of the build output.
// Each app owns <ArtifactRoot>/apps/<app>, holding its functions, static
// assets, cache entries and routing manifest.
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

// functionURLsByRoute joins the two namespaces a deploy names functions in:
// stack outputs are keyed by logical name, workers dispatch by route id.
func functionURLsByRoute(functions []*deploymentsv1.ManifestFunction, outputs []*deploymentsv1.ResourceOutput) map[string]string {
	urlByLogical := make(map[string]string)
	for _, o := range outputs {
		if fn := o.GetFunction(); fn != nil {
			urlByLogical[o.GetLogicalName()] = fn.GetUrl()
		}
	}

	result := make(map[string]string)
	for _, fn := range functions {
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
	prefix, err := assetPrefix(d.cfg, d.manifest)
	if err != nil {
		return edge.CacheStore{}, false, err
	}
	if prefix == "" {
		return edge.CacheStore{}, false, nil
	}

	isr := isrConfig{Bucket: d.cfg.AssetBucket, Prefix: prefix, Table: d.cfg.StateTable}
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

func productionDomain(cfg Config, manifest *deploymentsv1.Manifest) string {
	if cfg.Class != deploymentsv1.Environment_CLASS_PRODUCTION {
		return ""
	}
	return manifest.GetDomains()["production"]
}

// nextAppArtifactRoot locates the build output of this deploy's Next.js app —
// where its routing manifest and seeded cache entries live. It reports false
// when the manifest has no Next.js route, i.e. when this deploy has no ISR
// cache to scope.
func nextAppArtifactRoot(cfg Config, manifest *deploymentsv1.Manifest) (string, bool) {
	for _, fn := range manifest.GetFunctions() {
		if fn.GetFramework() == frameworkNext {
			return appArtifactRoot(cfg.ArtifactRoot, fn.GetApp()), true
		}
	}
	return "", false
}

// sanitizeWorkerName lowers an arbitrary identity into an edge deployment
// name: lowercase, every character outside [a-z0-9] replaced with '-',
// leading/trailing hyphens trimmed, and clamped to the 63-char limit. The result
// is deterministic so redeploys of the same project+env update the script in
// place.
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
	name := trimHyphens(string(buf))
	if len(name) > 63 {
		name = trimHyphens(name[:63])
	}
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
