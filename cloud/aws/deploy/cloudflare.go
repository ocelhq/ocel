package deploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"path"
	"strings"

	"github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"
	"github.com/cloudflare/cloudflare-go/v4/zones"
)

// nextWorkerCompatDate pins the Workers runtime compatibility date the worker is
// built against (mirrors workers/nextjs/wrangler.jsonc). nextWorkerCompatFlags
// enables the Node.js compatibility the bundled routing code relies on.
const nextWorkerCompatDate = "2026-07-13"

var nextWorkerCompatFlags = []string{"nodejs_compat"}

// nextWorkerObservability is the Workers observability settings every deployed
// worker ships with: logs (with per-invocation summaries) and OTel traces, both
// at 100% head sampling. It is uploaded as a field of the script metadata, the
// same way wrangler applies it, so no separate settings call is needed.
var nextWorkerObservability = map[string]any{
	"enabled":            true,
	"head_sampling_rate": 1,
	"logs":               map[string]any{"enabled": true, "invocation_logs": true},
	"traces":             map[string]any{"enabled": true},
}

// cfDeployer is the cloudflare-go-backed CloudflareDeployer. It performs the
// real multi-step worker upload (assets session -> asset batches -> script
// PUT -> custom-domain or workers.dev routing) and is exercised only
// end-to-end; the deploy orchestration is unit-tested against a fake through
// the CloudflareDeployer seam.
type cfDeployer struct {
	client *cloudflare.Client
}

// NewCloudflareDeployer builds a CloudflareDeployer whose API token is read from
// CLOUDFLARE_API_TOKEN by the cloudflare-go client.
func NewCloudflareDeployer() CloudflareDeployer {
	return &cfDeployer{client: cloudflare.NewClient()}
}

func (d *cfDeployer) DeployWorker(ctx context.Context, upload WorkerUpload) (WorkerResult, error) {
	assetsJWT, err := d.uploadAssets(ctx, upload)
	if err != nil {
		return WorkerResult{}, fmt.Errorf("upload assets: %w", err)
	}

	if err := d.putScript(ctx, upload, assetsJWT); err != nil {
		return WorkerResult{}, fmt.Errorf("put worker script: %w", err)
	}

	if err := d.reconcileCustomDomains(ctx, upload, upload.Domain); err != nil {
		return WorkerResult{}, err
	}
	url, err := d.setSubdomain(ctx, upload, upload.Domain == "")
	if err != nil {
		return WorkerResult{}, fmt.Errorf("set workers.dev subdomain: %w", err)
	}
	if upload.Domain != "" {
		url = "https://" + upload.Domain
	}
	return WorkerResult{URL: url}, nil
}

// uploadAssets registers the static-asset manifest, uploads the file batches the
// session asks for, and returns the completion JWT the script upload binds. When
// the worker has no static assets it returns an empty token and uploads nothing.
func (d *cfDeployer) uploadAssets(ctx context.Context, upload WorkerUpload) (string, error) {
	if len(upload.Assets) == 0 {
		return "", nil
	}

	manifest := make(map[string]workers.ScriptAssetUploadNewParamsManifest, len(upload.Assets))
	assetByHash := make(map[string]StaticAsset, len(upload.Assets))
	for _, a := range upload.Assets {
		manifest[a.Path] = workers.ScriptAssetUploadNewParamsManifest{
			Hash: cloudflare.F(a.Hash),
			Size: cloudflare.F(a.Size),
		}
		assetByHash[a.Hash] = a
	}

	session, err := d.client.Workers.Scripts.Assets.Upload.New(ctx, upload.ScriptName, workers.ScriptAssetUploadNewParams{
		AccountID: cloudflare.F(upload.AccountID),
		Manifest:  cloudflare.F(manifest),
	})
	if err != nil {
		return "", fmt.Errorf("create assets upload session: %w", err)
	}

	// When every file in the manifest is already present (e.g. a redeploy), the
	// session returns no buckets and its own JWT is the completion token. When
	// there are buckets, only a completed batch upload yields the completion
	// token — the session JWT merely authenticates the uploads.
	filesToUpload := 0
	for _, bucket := range session.Buckets {
		filesToUpload += len(bucket)
	}
	if filesToUpload == 0 {
		if session.JWT == "" {
			return "", fmt.Errorf("assets session returned no completion token")
		}
		return session.JWT, nil
	}

	completionJWT := ""
	for _, bucket := range session.Buckets {
		if len(bucket) == 0 {
			continue
		}
		body, contentType, err := buildAssetBatch(bucket, assetByHash)
		if err != nil {
			return "", err
		}
		res, err := d.client.Workers.Assets.Upload.New(ctx, workers.AssetUploadNewParams{
			AccountID: cloudflare.F(upload.AccountID),
			Base64:    cloudflare.F(workers.AssetUploadNewParamsBase64True),
		}, option.WithRequestBody(contentType, body), option.WithHeader("Authorization", "Bearer "+session.JWT))
		if err != nil {
			return "", fmt.Errorf("upload asset batch: %w", err)
		}
		if res.JWT != "" {
			completionJWT = res.JWT
		}
	}
	if completionJWT == "" {
		return "", fmt.Errorf("asset upload returned no completion token")
	}
	return completionJWT, nil
}

// buildAssetBatch encodes one bucket of files as the multipart/form-data body
// the assets upload endpoint expects: one part per file, named and filenamed by
// its content hash, carrying the base64-encoded contents (the ?base64=true query
// tells Cloudflare the parts are base64). An unknown extension maps to
// "application/null" — Cloudflare's sentinel for "serve without a Content-Type",
// mirroring wrangler.
func buildAssetBatch(bucket []string, assetByHash map[string]StaticAsset) ([]byte, string, error) {
	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)
	for _, hash := range bucket {
		asset := assetByHash[hash]
		contentType := mime.TypeByExtension(path.Ext(asset.Path))
		if contentType == "" {
			contentType = "application/null"
		}
		encoded := base64.StdEncoding.EncodeToString(asset.Content)
		if err := writePart(w, hash, hash, contentType, []byte(encoded)); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// putScript uploads the worker as a multipart module-syntax script: a metadata
// part describing the bindings, assets, and compatibility, plus one part per
// module (the entrypoint and the routing manifest). The generated Update method
// only serializes metadata JSON, so the multipart body is built by hand and
// swapped in via WithRequestBody.
func (d *cfDeployer) putScript(ctx context.Context, upload WorkerUpload, assetsJWT string) error {
	body, contentType, err := buildScriptMultipart(upload, assetsJWT)
	if err != nil {
		return err
	}

	_, err = d.client.Workers.Scripts.Update(ctx, upload.ScriptName, workers.ScriptUpdateParams{
		AccountID: cloudflare.F(upload.AccountID),
	}, option.WithRequestBody(contentType, body))
	return err
}

// buildScriptMultipart assembles the worker upload's multipart/form-data body
// and its content type.
func buildScriptMultipart(upload WorkerUpload, assetsJWT string) ([]byte, string, error) {
	// Cloudflare rejects an assets binding without a completed assets upload, so
	// the binding and the assets metadata are gated on the same token: present
	// together or absent together.
	includeAssets := assetsJWT != ""
	metadata := map[string]any{
		"main_module":         upload.Main.Name,
		"compatibility_date":  nextWorkerCompatDate,
		"compatibility_flags": nextWorkerCompatFlags,
		"observability":       nextWorkerObservability,
		"bindings":            scriptBindings(upload, includeAssets),
	}
	if includeAssets {
		metadata["assets"] = map[string]any{
			"jwt": assetsJWT,
			// The worker is the authoritative router: it always runs and delegates
			// to the Assets binding itself, rather than Cloudflare serving assets
			// ahead of the worker.
			"config": map[string]any{"run_worker_first": true},
		}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, "", fmt.Errorf("marshal worker metadata: %w", err)
	}

	buf := &bytes.Buffer{}
	w := multipart.NewWriter(buf)

	if err := writePart(w, "metadata", "", "application/json", metadataJSON); err != nil {
		return nil, "", err
	}
	for _, mod := range append([]WorkerModule{upload.Main}, upload.Modules...) {
		if err := writePart(w, mod.Name, mod.Name, mod.ContentType, mod.Content); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// scriptBindings is the worker's binding set: the Assets Fetcher (only when
// assets were uploaded) plus one plain-text binding per var (e.g.
// FUNCTION_URLS).
func scriptBindings(upload WorkerUpload, includeAssets bool) []map[string]any {
	bindings := []map[string]any{}
	if includeAssets && upload.AssetBinding != "" {
		bindings = append(bindings, map[string]any{
			"type": "assets",
			"name": upload.AssetBinding,
		})
	}
	for name, text := range upload.Vars {
		bindings = append(bindings, map[string]any{
			"type": "plain_text",
			"name": name,
			"text": text,
		})
	}
	return bindings
}

// writePart writes one multipart form part. A non-empty filename marks the part
// as a module file rather than a plain field.
func writePart(w *multipart.Writer, name, filename, contentType string, content []byte) error {
	header := textproto.MIMEHeader{}
	if filename != "" {
		header.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q; filename=%q", name, filename))
	} else {
		header.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q", name))
	}
	header.Set("Content-Type", contentType)
	part, err := w.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write(content)
	return err
}

// setSubdomain returns the worker's public workers.dev URL when enabling, or ""
// when disabling.
func (d *cfDeployer) setSubdomain(ctx context.Context, upload WorkerUpload, enabled bool) (string, error) {
	if _, err := d.client.Workers.Scripts.Subdomain.New(ctx, upload.ScriptName, workers.ScriptSubdomainNewParams{
		AccountID:       cloudflare.F(upload.AccountID),
		Enabled:         cloudflare.F(enabled),
		PreviewsEnabled: cloudflare.F(false),
	}); err != nil {
		return "", err
	}
	if !enabled {
		return "", nil
	}

	account, err := d.client.Workers.Subdomains.Get(ctx, workers.SubdomainGetParams{
		AccountID: cloudflare.F(upload.AccountID),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", upload.ScriptName, account.Subdomain), nil
}

// reconcileCustomDomains makes the worker's attached custom domains exactly
// {desired}, or none when desired is empty: it detaches every attached hostname
// that isn't desired, then attaches desired if it isn't already. The desired
// hostname's zone is resolved from the account's zones.
func (d *cfDeployer) reconcileCustomDomains(ctx context.Context, upload WorkerUpload, desired string) error {
	attached := d.client.Workers.Domains.ListAutoPaging(ctx, workers.DomainListParams{
		AccountID: cloudflare.F(upload.AccountID),
		Service:   cloudflare.F(upload.ScriptName),
	})
	desiredAttached := false
	for attached.Next() {
		dom := attached.Current()
		if dom.Hostname == desired {
			desiredAttached = true
			continue
		}
		if err := d.client.Workers.Domains.Delete(ctx, dom.ID, workers.DomainDeleteParams{
			AccountID: cloudflare.F(upload.AccountID),
		}); err != nil {
			return fmt.Errorf("detach custom domain %q: %w", dom.Hostname, err)
		}
	}
	if err := attached.Err(); err != nil {
		return fmt.Errorf("list custom domains: %w", err)
	}
	if desired == "" || desiredAttached {
		return nil
	}

	zoneID, err := d.resolveZoneID(ctx, upload.AccountID, desired)
	if err != nil {
		return err
	}
	if _, err := d.client.Workers.Domains.Update(ctx, workers.DomainUpdateParams{
		AccountID:   cloudflare.F(upload.AccountID),
		Environment: cloudflare.F("production"),
		Hostname:    cloudflare.F(desired),
		Service:     cloudflare.F(upload.ScriptName),
		ZoneID:      cloudflare.F(zoneID),
	}); err != nil {
		return fmt.Errorf("attach custom domain %q: %w", desired, err)
	}
	return nil
}

// resolveZoneID finds the account zone whose name is the longest suffix of
// hostname (e.g. "acme.com" for "app.acme.com"). A hostname with no owning zone
// in the account is a hard error: the deploy cannot serve it.
func (d *cfDeployer) resolveZoneID(ctx context.Context, accountID, hostname string) (string, error) {
	owned := d.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Account: cloudflare.F(zones.ZoneListParamsAccount{ID: cloudflare.F(accountID)}),
	})
	bestID, bestName := "", ""
	for owned.Next() {
		z := owned.Current()
		if zoneOwns(hostname, z.Name) && len(z.Name) > len(bestName) {
			bestID, bestName = z.ID, z.Name
		}
	}
	if err := owned.Err(); err != nil {
		return "", fmt.Errorf("list zones: %w", err)
	}
	if bestID == "" {
		return "", fmt.Errorf("no Cloudflare zone in this account owns %q — add its zone to the account whose CLOUDFLARE_API_TOKEN you provided", hostname)
	}
	return bestID, nil
}

func zoneOwns(hostname, zone string) bool {
	return hostname == zone || strings.HasSuffix(hostname, "."+zone)
}
