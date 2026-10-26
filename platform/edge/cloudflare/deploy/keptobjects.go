package cloudflare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/r2"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	storeObjectsBinding = "OBJECTS"
	entryObjectsBinding = "OCEL_OBJECTS"
	objectsBucketStem   = "objects"

	objectBatchBytes = 16 << 20
	maxObjectBytes   = 96 << 20
)

func (n originNames) objectsBucket(tier environment.Tier) (string, error) {
	return n.accountName("objects bucket", tier, objectsBucketStem)
}

func (p *cloudflare) keptBucketName(names originNames, tier environment.Tier) string {
	if !p.KeepsObjects {
		return ""
	}
	bucket, err := names.objectsBucket(tier)
	if err != nil {
		return ""
	}
	return bucket
}

func withKeptObjects(worker workerScript, bucket string) workerScript {
	if bucket == "" {
		return worker
	}
	buckets := make(map[string]string, len(worker.R2Buckets)+1)
	for name, held := range worker.R2Buckets {
		buckets[name] = held
	}
	buckets[entryObjectsBinding] = bucket
	worker.R2Buckets = buckets
	return worker
}

func (p *cloudflare) ensureObjectsBucket(ctx context.Context, accountID, bucket string) error {
	_, err := p.client.R2.Buckets.Get(ctx, bucket, r2.BucketGetParams{AccountID: cf.F(accountID)})
	if err == nil {
		return nil
	}
	if !hasStatus(err, http.StatusNotFound) {
		return fmt.Errorf("read the R2 bucket %s: %w", bucket, err)
	}
	if _, err := p.client.R2.Buckets.New(ctx, r2.BucketNewParams{AccountID: cf.F(accountID), Name: cf.F(bucket)}); err != nil && !hasStatus(err, http.StatusConflict) {
		return fmt.Errorf("create the R2 bucket %s, which holds what this origin's releases serve from the edge: %w", bucket, err)
	}
	return nil
}

func (p *cloudflare) deleteObjectsBucket(ctx context.Context, accountID, bucket string) error {
	_, err := p.client.R2.Buckets.Delete(ctx, bucket, r2.BucketDeleteParams{AccountID: cf.F(accountID)})
	if err == nil || hasStatus(err, http.StatusNotFound) {
		return nil
	}
	return fmt.Errorf("delete the R2 bucket %s: %w", bucket, err)
}

type KeptObjects struct{ p *cloudflare }

func NewKeptObjects(namespace string, origin Origin) KeptObjects {
	origin.KeepsObjects = true
	return KeptObjects{p: newCloudflare(namespace, origin, Options{})}
}

type KeptObject struct {
	Key         string
	ContentType string
	Body        []byte
}

func ComputeStoreVersion() string {
	sum := sha256.New()
	fmt.Fprintln(sum, compatDate, compatFlags, storeObjectsBinding, releasesStoreWorker)
	sum.Write(releasesStoreBundle)
	return hex.EncodeToString(sum.Sum(nil))
}

func (k KeptObjects) EnsureStore(ctx context.Context, tier environment.Tier, credential string) (edge.ReleasesStore, error) {
	accountID, err := bootstrapCredentials()
	if err != nil {
		return edge.ReleasesStore{}, err
	}
	names, err := k.p.resolveNames(ctx)
	if err != nil {
		return edge.ReleasesStore{}, err
	}
	if err := k.p.recordBootstrapAccount(ctx, tier, accountID); err != nil {
		return edge.ReleasesStore{}, err
	}
	bucket, err := names.objectsBucket(tier)
	if err != nil {
		return edge.ReleasesStore{}, err
	}
	if err := k.p.ensureObjectsBucket(ctx, accountID, bucket); err != nil {
		return edge.ReleasesStore{}, err
	}
	state, err := k.p.readState(ctx, accountID, names, tier)
	if err != nil {
		return edge.ReleasesStore{}, err
	}
	var store edge.ReleasesStore
	for _, worker := range state.workers {
		worker.secretPresent = false
		if store, err = k.p.ensureWorkerWith(ctx, accountID, worker, credential); err != nil {
			return edge.ReleasesStore{}, fmt.Errorf("install %s: %w", worker.what, err)
		}
	}
	return store, nil
}

func (k KeptObjects) Put(ctx context.Context, store edge.ReleasesStore, slug string, objects []KeptObject) error {
	var batch []KeptObject
	batchBytes := 0
	for _, object := range objects {
		if len(object.Body) > maxObjectBytes {
			return refusal.Refuse(refusal.CodeInvalid,
				"%s is %d bytes, over the %d bytes the edge keeps of one file: shrink it", object.Key, len(object.Body), maxObjectBytes)
		}
		if batchBytes+len(object.Body) > objectBatchBytes && len(batch) > 0 {
			if err := k.putBatch(ctx, store, slug, batch); err != nil {
				return err
			}
			batch, batchBytes = nil, 0
		}
		batch = append(batch, object)
		batchBytes += len(object.Body)
	}
	if len(batch) == 0 {
		return nil
	}
	return k.putBatch(ctx, store, slug, batch)
}

func (k KeptObjects) putBatch(ctx context.Context, store edge.ReleasesStore, slug string, batch []KeptObject) error {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for _, object := range batch {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf("form-data; name=%q; filename=%q", object.Key, object.Key))
		header.Set("Content-Type", object.ContentType)
		part, err := form.CreatePart(header)
		if err != nil {
			return err
		}
		if _, err := part.Write(object.Body); err != nil {
			return err
		}
	}
	if err := form.Close(); err != nil {
		return err
	}
	return k.send(ctx, store, slug, "/objects", form.FormDataContentType(), body.Bytes())
}

func (k KeptObjects) Remove(ctx context.Context, store edge.ReleasesStore, slug string, prefixes []string) error {
	if len(prefixes) == 0 {
		return nil
	}
	encoded, err := json.Marshal(map[string][]string{"prefixes": prefixes})
	if err != nil {
		return err
	}
	return k.send(ctx, store, slug, "/objects/remove", "application/json", encoded)
}

func (k KeptObjects) send(ctx context.Context, store edge.ReleasesStore, slug, subpath, contentType string, body []byte) error {
	if store.Endpoint == "" || store.BootstrapCredential == "" {
		return fmt.Errorf("%w: it names no endpoint or credential to keep this origin's objects with", edge.ErrStoreAbsent)
	}
	for attempt := 0; ; attempt++ {
		res, err := k.attempt(ctx, store, slug, subpath, contentType, body)
		if attempt == storeMaxAttempts-1 || !storeRetryable(res, err) {
			return err
		}
		if waitErr := waitBeforeRetry(ctx, storeRetryDelay(res, attempt, retryJitter())); waitErr != nil {
			return err
		}
	}
}

func (k KeptObjects) attempt(ctx context.Context, store edge.ReleasesStore, slug, subpath, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, store.Endpoint+"/"+slug+subpath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errStoreRequestUnbuildable, err)
	}
	req.Header.Set("Authorization", "Bearer "+store.BootstrapCredential)
	req.Header.Set("Content-Type", contentType)
	res, err := k.p.store.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("call releases store POST %s: %w", subpath, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		answered, _ := io.ReadAll(res.Body)
		return res, fmt.Errorf("releases store POST %s: status %d: %s", subpath, res.StatusCode, answered)
	}
	return res, nil
}

func (k KeptObjects) Teardown(ctx context.Context, tier environment.Tier) error {
	accountID, err := k.p.readTierAccount(ctx, tier, "remove the objects the edge keeps for this origin")
	if err != nil {
		return err
	}
	names, err := k.p.resolveNames(ctx)
	if err != nil {
		return err
	}
	storeScript, err := names.storeScript(tier)
	if err != nil {
		return err
	}
	if err := k.p.deleteScript(ctx, accountID, storeScript); err != nil {
		return fmt.Errorf("delete worker %q: %w", storeScript, err)
	}
	bucket, err := names.objectsBucket(tier)
	if err != nil {
		return err
	}
	return k.p.deleteObjectsBucket(ctx, accountID, bucket)
}
