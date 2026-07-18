// Package cloudflare is the Cloudflare Workers edge: it uploads an assembled
// worker as a Workers script with its static assets, and routes it on a custom
// domain or the account's workers.dev subdomain.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path"
	"strings"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"
	"github.com/cloudflare/cloudflare-go/v4/zones"

	"github.com/ocelhq/ocel/cloud/edge"
)

// envAccountID names the Cloudflare account workers are deployed into. The API
// token is read by the SDK client from CLOUDFLARE_API_TOKEN.
const envAccountID = "CLOUDFLARE_ACCOUNT_ID"

// compatDate pins the Workers runtime compatibility date uploaded scripts are
// built against (mirrors workers/nextjs/wrangler.jsonc). compatFlags enables the
// Node.js compatibility the bundled routing code relies on.
const compatDate = "2026-07-13"

var compatFlags = []string{"nodejs_compat"}

// observability is the Workers observability settings every deployed worker
// ships with: logs (with per-invocation summaries) and OTel traces, both at 100%
// head sampling. It is uploaded as a field of the script metadata, the same way
// wrangler applies it, so no separate settings call is needed.
var observability = map[string]any{
	"enabled":            true,
	"head_sampling_rate": 1,
	"logs":               map[string]any{"enabled": true, "invocation_logs": true},
	"traces":             map[string]any{"enabled": true},
}

// provider is the cloudflare-go-backed edge.Provider. It performs the real
// multi-step worker upload (assets session -> asset batches -> script PUT ->
// custom-domain or workers.dev routing) and is exercised only end-to-end; the
// provider-side deploy orchestration is unit-tested against a fake through the
// edge.Provider seam.
type provider struct {
	client *cf.Client
}

// New builds the Cloudflare edge. Its API token is read from
// CLOUDFLARE_API_TOKEN by the cloudflare-go client.
func New() edge.Provider {
	return &provider{client: cf.NewClient()}
}

func (p *provider) Kind() edge.Kind { return edge.KindCloudflare }

// Bootstrap reports Cloudflare's trust posture. Cloudflare runs in its own
// account, outside any cloud provider's trust boundary, so the provider must
// mint static credentials for it. It provisions nothing of its own and so
// offers nothing.
func (p *provider) Bootstrap(context.Context) (edge.BootstrapOutput, error) {
	return edge.BootstrapOutput{Trust: edge.TrustExternal}, nil
}

// FindApp reports whether a Workers script exists under name. A 404 is the
// answer "no", not a failure.
func (p *provider) FindApp(ctx context.Context, name string) (bool, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return false, fmt.Errorf("%s is not set; it is required to query the Cloudflare edge", envAccountID)
	}
	_, err := p.client.Workers.Scripts.Settings.Get(ctx, name, workers.ScriptSettingGetParams{
		AccountID: cf.F(accountID),
	})
	var apiErr *cf.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

func (p *provider) DeployApp(ctx context.Context, app edge.AppDeployment) (edge.AppResult, error) {
	accountID := os.Getenv(envAccountID)
	if accountID == "" {
		return edge.AppResult{}, fmt.Errorf("%s is not set; it is required to deploy to the Cloudflare edge", envAccountID)
	}
	up := upload{accountID: accountID, scriptName: app.Name, worker: app.Worker}

	assetsJWT, err := p.uploadAssets(ctx, up)
	if err != nil {
		return edge.AppResult{}, fmt.Errorf("upload assets: %w", err)
	}

	if err := p.putScript(ctx, up, assetsJWT); err != nil {
		return edge.AppResult{}, fmt.Errorf("put worker script: %w", err)
	}

	if err := p.reconcileCustomDomains(ctx, up, app.Domain); err != nil {
		return edge.AppResult{}, err
	}
	url, err := p.setSubdomain(ctx, up, app.Domain == "")
	if err != nil {
		return edge.AppResult{}, fmt.Errorf("set workers.dev subdomain: %w", err)
	}
	if app.Domain != "" {
		url = "https://" + app.Domain
	}
	return edge.AppResult{URL: url}, nil
}

// upload is one app deployment resolved against the Cloudflare account it lands
// in.
type upload struct {
	accountID  string
	scriptName string
	worker     edge.Worker
}

// uploadAssets registers the static-asset manifest, uploads the file batches the
// session asks for, and returns the completion JWT the script upload binds. When
// the worker has no static assets it returns an empty token and uploads nothing.
func (p *provider) uploadAssets(ctx context.Context, up upload) (string, error) {
	if len(up.worker.Assets) == 0 {
		return "", nil
	}

	manifest := make(map[string]workers.ScriptAssetUploadNewParamsManifest, len(up.worker.Assets))
	assetByHash := make(map[string]edge.StaticAsset, len(up.worker.Assets))
	for _, a := range up.worker.Assets {
		manifest[a.Path] = workers.ScriptAssetUploadNewParamsManifest{
			Hash: cf.F(a.Hash),
			Size: cf.F(a.Size),
		}
		assetByHash[a.Hash] = a
	}

	session, err := p.client.Workers.Scripts.Assets.Upload.New(ctx, up.scriptName, workers.ScriptAssetUploadNewParams{
		AccountID: cf.F(up.accountID),
		Manifest:  cf.F(manifest),
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
		res, err := p.client.Workers.Assets.Upload.New(ctx, workers.AssetUploadNewParams{
			AccountID: cf.F(up.accountID),
			Base64:    cf.F(workers.AssetUploadNewParamsBase64True),
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
func buildAssetBatch(bucket []string, assetByHash map[string]edge.StaticAsset) ([]byte, string, error) {
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
// module (the entrypoint and any siblings). The generated Update method only
// serializes metadata JSON, so the multipart body is built by hand and swapped
// in via WithRequestBody.
func (p *provider) putScript(ctx context.Context, up upload, assetsJWT string) error {
	body, contentType, err := buildScriptMultipart(up.worker, assetsJWT)
	if err != nil {
		return err
	}

	_, err = p.client.Workers.Scripts.Update(ctx, up.scriptName, workers.ScriptUpdateParams{
		AccountID: cf.F(up.accountID),
	}, option.WithRequestBody(contentType, body))
	return err
}

// buildScriptMultipart assembles the worker upload's multipart/form-data body
// and its content type.
func buildScriptMultipart(worker edge.Worker, assetsJWT string) ([]byte, string, error) {
	// Cloudflare rejects an assets binding without a completed assets upload, so
	// the binding and the assets metadata are gated on the same token: present
	// together or absent together.
	includeAssets := assetsJWT != ""
	metadata := map[string]any{
		"main_module":         worker.Main.Name,
		"compatibility_date":  compatDate,
		"compatibility_flags": compatFlags,
		"observability":       observability,
		"bindings":            scriptBindings(worker, includeAssets),
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
	for _, mod := range append([]edge.WorkerModule{worker.Main}, worker.Modules...) {
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
// assets were uploaded), one plain-text binding per var, and one secret_text
// binding per secret — values that must never surface in plaintext metadata.
func scriptBindings(worker edge.Worker, includeAssets bool) []map[string]any {
	bindings := []map[string]any{}
	if includeAssets && worker.AssetBinding != "" {
		bindings = append(bindings, map[string]any{
			"type": "assets",
			"name": worker.AssetBinding,
		})
	}
	for name, text := range worker.Vars {
		bindings = append(bindings, map[string]any{
			"type": "plain_text",
			"name": name,
			"text": text,
		})
	}
	for name, text := range worker.Secrets {
		bindings = append(bindings, map[string]any{
			"type": "secret_text",
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
func (p *provider) setSubdomain(ctx context.Context, up upload, enabled bool) (string, error) {
	if _, err := p.client.Workers.Scripts.Subdomain.New(ctx, up.scriptName, workers.ScriptSubdomainNewParams{
		AccountID:       cf.F(up.accountID),
		Enabled:         cf.F(enabled),
		PreviewsEnabled: cf.F(false),
	}); err != nil {
		return "", err
	}
	if !enabled {
		return "", nil
	}

	account, err := p.client.Workers.Subdomains.Get(ctx, workers.SubdomainGetParams{
		AccountID: cf.F(up.accountID),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", up.scriptName, account.Subdomain), nil
}

// reconcileCustomDomains makes the worker's attached custom domains exactly
// {desired}, or none when desired is empty: it detaches every attached hostname
// that isn't desired, then attaches desired if it isn't already. The desired
// hostname's zone is resolved from the account's zones.
func (p *provider) reconcileCustomDomains(ctx context.Context, up upload, desired string) error {
	attached := p.client.Workers.Domains.ListAutoPaging(ctx, workers.DomainListParams{
		AccountID: cf.F(up.accountID),
		Service:   cf.F(up.scriptName),
	})
	desiredAttached := false
	for attached.Next() {
		dom := attached.Current()
		if dom.Hostname == desired {
			desiredAttached = true
			continue
		}
		if err := p.client.Workers.Domains.Delete(ctx, dom.ID, workers.DomainDeleteParams{
			AccountID: cf.F(up.accountID),
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

	zoneID, err := p.resolveZoneID(ctx, up.accountID, desired)
	if err != nil {
		return err
	}
	if _, err := p.client.Workers.Domains.Update(ctx, workers.DomainUpdateParams{
		AccountID:   cf.F(up.accountID),
		Environment: cf.F("production"),
		Hostname:    cf.F(desired),
		Service:     cf.F(up.scriptName),
		ZoneID:      cf.F(zoneID),
	}); err != nil {
		return fmt.Errorf("attach custom domain %q: %w", desired, err)
	}
	return nil
}

// resolveZoneID finds the account zone whose name is the longest suffix of
// hostname (e.g. "acme.com" for "app.acme.com"). A hostname with no owning zone
// in the account is a hard error: the deploy cannot serve it.
func (p *provider) resolveZoneID(ctx context.Context, accountID, hostname string) (string, error) {
	owned := p.client.Zones.ListAutoPaging(ctx, zones.ZoneListParams{
		Account: cf.F(zones.ZoneListParamsAccount{ID: cf.F(accountID)}),
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
