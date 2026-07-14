package deploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/textproto"

	"github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/option"
	"github.com/cloudflare/cloudflare-go/v4/workers"
)

// nextWorkerCompatDate pins the Workers runtime compatibility date the worker is
// built against (mirrors workers/nextjs/wrangler.jsonc). nextWorkerCompatFlags
// enables the Node.js compatibility the bundled routing code relies on.
const nextWorkerCompatDate = "2026-07-13"

var nextWorkerCompatFlags = []string{"nodejs_compat"}

// cfDeployer is the cloudflare-go-backed CloudflareDeployer. It performs the
// real multi-step worker upload (assets session -> asset batches -> script
// PUT -> workers.dev enablement) and is exercised only end-to-end; the deploy
// orchestration is unit-tested against a fake through the CloudflareDeployer
// seam.
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

	url, err := d.enableSubdomain(ctx, upload)
	if err != nil {
		return WorkerResult{}, fmt.Errorf("enable workers.dev subdomain: %w", err)
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
	contentByHash := make(map[string][]byte, len(upload.Assets))
	for _, a := range upload.Assets {
		manifest[a.Path] = workers.ScriptAssetUploadNewParamsManifest{
			Hash: cloudflare.F(a.Hash),
			Size: cloudflare.F(a.Size),
		}
		contentByHash[a.Hash] = a.Content
	}

	session, err := d.client.Workers.Scripts.Assets.Upload.New(ctx, upload.ScriptName, workers.ScriptAssetUploadNewParams{
		AccountID: cloudflare.F(upload.AccountID),
		Manifest:  cloudflare.F(manifest),
	})
	if err != nil {
		return "", fmt.Errorf("create assets upload session: %w", err)
	}

	// When every file in the manifest is already present (e.g. a redeploy), the
	// session returns no buckets to upload and its own JWT is the completion
	// token. Otherwise each bucket is uploaded and the final response carries the
	// completion token; the session JWT authenticates those uploads.
	completionJWT := session.JWT
	for _, bucket := range session.Buckets {
		if len(bucket) == 0 {
			continue
		}
		body := make(map[string]string, len(bucket))
		for _, hash := range bucket {
			body[hash] = base64.StdEncoding.EncodeToString(contentByHash[hash])
		}
		res, err := d.client.Workers.Assets.Upload.New(ctx, workers.AssetUploadNewParams{
			AccountID: cloudflare.F(upload.AccountID),
			Base64:    cloudflare.F(workers.AssetUploadNewParamsBase64True),
			Body:      body,
		}, option.WithHeader("Authorization", "Bearer "+session.JWT))
		if err != nil {
			return "", fmt.Errorf("upload asset batch: %w", err)
		}
		if res.JWT != "" {
			completionJWT = res.JWT
		}
	}
	return completionJWT, nil
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

// enableSubdomain turns on the worker's workers.dev route and returns its public
// URL, composed from the script name and the account's workers.dev subdomain.
func (d *cfDeployer) enableSubdomain(ctx context.Context, upload WorkerUpload) (string, error) {
	if _, err := d.client.Workers.Scripts.Subdomain.New(ctx, upload.ScriptName, workers.ScriptSubdomainNewParams{
		AccountID:       cloudflare.F(upload.AccountID),
		Enabled:         cloudflare.F(true),
		PreviewsEnabled: cloudflare.F(false),
	}); err != nil {
		return "", err
	}

	account, err := d.client.Workers.Subdomains.Get(ctx, workers.SubdomainGetParams{
		AccountID: cloudflare.F(upload.AccountID),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", upload.ScriptName, account.Subdomain), nil
}
