package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/ocelhq/ocel/cloud/edge"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// frameworkNext marks a ManifestFunction whose routes are fronted by the
// Cloudflare Next.js worker. It matches the value the adapter writes into each
// function's config.json.
const frameworkNext = "next"

// nextWorkerAssetBinding is the binding name the compiled worker reads its
// Workers Assets Fetcher from (env.ASSETS).
const nextWorkerAssetBinding = "ASSETS"

// nextWorkerURLsVar is the plain-text var the worker parses its route-id ->
// Function URL map from (env.FUNCTION_URLS).
const nextWorkerURLsVar = "FUNCTION_URLS"

// The edge reader bindings the worker signs its direct ISR reads with. The
// access key id and region are plain_text; the secret access key is secret_text.
// These names must match what the worker's readInterceptionConfig reads; the
// OCEL_ISR_* / OCEL_STATE_TABLE coordinates come from isrConfig.env().
const (
	edgeAccessKeyIDVar = "OCEL_EDGE_ACCESS_KEY_ID"
	edgeSecretKeyVar   = "OCEL_EDGE_SECRET_KEY"
	edgeRegionVar      = "OCEL_AWS_REGION"
)

// nextWorkerOutputName is the logical name the deployed worker's public URL is
// reported under in the stack outputs.
const nextWorkerOutputName = "next-worker"

// envNextWorkerPath points at the compiled worker entrypoint bundle
// (dist/index.js), exported by the npm launcher.
const envNextWorkerPath = "OCEL_NEXT_WORKER_PATH"

// buildFunctionURLs maps each Next function's route id to its deployed Function
// URL. The manifest carries route_id (the framework-native identity the worker
// dispatches to) separately from the infra-safe logical_name the URL output is
// keyed by, so this join is the single place the two namespaces meet. Non-Next
// functions and functions without a URL are skipped.
func buildFunctionURLs(functions []*deploymentsv1.ManifestFunction, outputs []*deploymentsv1.ResourceOutput) map[string]string {
	urlByLogical := make(map[string]string)
	for _, o := range outputs {
		if fn := o.GetFunction(); fn != nil {
			urlByLogical[o.GetLogicalName()] = fn.GetUrl()
		}
	}

	result := make(map[string]string)
	for _, fn := range functions {
		if fn.GetFramework() != frameworkNext {
			continue
		}
		url := urlByLogical[fn.GetLogicalName()]
		if url == "" {
			continue
		}
		key := fn.GetRouteId()
		if key == "" {
			key = fn.GetLogicalName()
		}
		result[key] = url
	}
	return result
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

// hashAsset computes the content hash an edge's assets upload session keys a
// file by: the SHA-256 of the base64-encoded contents concatenated with the
// file extension (no leading dot), hex-encoded and truncated to 32 characters.
// This mirrors wrangler's algorithm; a mismatch would make the session reject
// the upload.
func hashAsset(content []byte, ext string) string {
	sum := sha256.Sum256([]byte(base64.StdEncoding.EncodeToString(content) + ext))
	return hex.EncodeToString(sum[:])[:32]
}

// collectStaticAssets reads every file under dir into a StaticAsset carrying its
// URL path, contents, content hash, and size. A missing directory yields no
// assets. Paths use forward slashes rooted at "/" regardless of host OS.
func collectStaticAssets(dir string) ([]edge.StaticAsset, error) {
	var assets []edge.StaticAsset
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == dir {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		urlPath := "/" + filepath.ToSlash(rel)
		ext := path.Ext(urlPath)
		if ext != "" {
			ext = ext[1:] // drop the leading dot
		}
		assets = append(assets, edge.StaticAsset{
			Path:    urlPath,
			Content: content,
			Hash:    hashAsset(content, ext),
			Size:    int64(len(content)),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return assets, nil
}

// deployNextWorker creates or updates the edge worker fronting a project's
// Next.js app: it builds the route-id -> Function URL map from the manifest and
// stack outputs, reads the compiled worker, routing manifest, and static assets
// off disk, and hands them to the configured edge. It is a no-op returning no
// outputs when the manifest has no Next function. A missing edge or worker
// bundle is a hard error, since a Next app can't be served without them.
func deployNextWorker(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, outputs []*deploymentsv1.ResourceOutput, progress func(string)) ([]*deploymentsv1.ResourceOutput, error) {
	if !hasNextFunction(manifest.GetFunctions()) {
		return nil, nil
	}

	if cfg.Edge == nil {
		return nil, fmt.Errorf("project has a Next.js app but no edge is configured")
	}
	workerPath := os.Getenv(envNextWorkerPath)
	if workerPath == "" {
		return nil, fmt.Errorf("%s is not set; the ocel CLI must be run through its npm launcher", envNextWorkerPath)
	}

	mainContent, err := os.ReadFile(workerPath)
	if err != nil {
		return nil, fmt.Errorf("read next worker bundle: %w", err)
	}
	manifestContent, err := os.ReadFile(filepath.Join(cfg.ArtifactRoot, "routing-manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("read routing manifest: %w", err)
	}
	assets, err := collectStaticAssets(filepath.Join(cfg.ArtifactRoot, "static"))
	if err != nil {
		return nil, fmt.Errorf("collect static assets: %w", err)
	}

	functionURLs, err := json.Marshal(buildFunctionURLs(manifest.GetFunctions(), outputs))
	if err != nil {
		return nil, fmt.Errorf("marshal function urls: %w", err)
	}

	vars := map[string]string{nextWorkerURLsVar: string(functionURLs)}
	secrets, err := interceptionBindings(cfg, manifest, vars)
	if err != nil {
		return nil, err
	}

	if progress != nil {
		progress("Deploying Next.js worker to the edge")
	}
	result, err := cfg.Edge.DeployApp(ctx, edge.AppDeployment{
		Name:   sanitizeWorkerName("ocel-" + cfg.StackName),
		Domain: productionDomain(cfg, manifest),
		Worker: edge.Worker{
			Main: edge.WorkerModule{
				Name:        "index.js",
				ContentType: "application/javascript+module",
				Content:     mainContent,
			},
			Modules: []edge.WorkerModule{{
				Name: "routing-manifest.json",
				// Uploaded as a text module (no edge module upload has a JSON
				// type); the worker JSON.parses its string default export.
				ContentType: "text/plain",
				Content:     manifestContent,
			}},
			Vars:         vars,
			Secrets:      secrets,
			AssetBinding: nextWorkerAssetBinding,
			Assets:       assets,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("deploy next worker: %w", err)
	}

	return []*deploymentsv1.ResourceOutput{
		collectFunctionOutput(nextWorkerOutputName, result.URL),
	}, nil
}

// interceptionBindings adds the edge-read bindings the worker needs to serve ISR
// directly from S3+DynamoDB: the store coordinates (isrConfig.env) and region +
// access key id go onto vars as plain_text, and returns the secret access key as
// a secret_text binding. It is a no-op — no bindings added, nil secrets — unless
// the substrate has edge credentials AND the app has a prerender asset prefix, so
// an older bootstrap or a non-Next deploy leaves the worker forwarding to the
// Lambda exactly as before. The injected env-var names must match the worker's
// readInterceptionConfig.
func interceptionBindings(cfg Config, manifest *deploymentsv1.Manifest, vars map[string]string) (map[string]string, error) {
	if cfg.EdgeAccessKeyID == "" || cfg.EdgeSecretKey == "" {
		return nil, nil
	}
	prefix, err := assetPrefix(cfg, manifest)
	if err != nil {
		return nil, err
	}
	if prefix == "" {
		return nil, nil
	}

	isr := isrConfig{Bucket: cfg.AssetBucket, Prefix: prefix, Table: cfg.StateTable}
	for k, v := range isr.env() {
		vars[k] = v
	}
	vars[edgeAccessKeyIDVar] = cfg.EdgeAccessKeyID
	vars[edgeRegionVar] = cfg.Region

	return map[string]string{edgeSecretKeyVar: cfg.EdgeSecretKey}, nil
}

func productionDomain(cfg Config, manifest *deploymentsv1.Manifest) string {
	if cfg.Class != deploymentsv1.Environment_CLASS_PRODUCTION {
		return ""
	}
	return manifest.GetDomains()["production"]
}

// hasNextFunction reports whether any function in the manifest is a Next.js
// route, i.e. whether a worker needs deploying.
func hasNextFunction(functions []*deploymentsv1.ManifestFunction) bool {
	for _, fn := range functions {
		if fn.GetFramework() == frameworkNext {
			return true
		}
	}
	return false
}
