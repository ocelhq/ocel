package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// The origin record: the one thing in the substrate that says where a build's
// routes are actually served from.
//
// The account's ISR revalidator is rendered once, at provider-install time, by
// cloud/aws/bootstrap; the Function URLs it has to trigger are created by this
// package on every deploy, with ids nobody can know at bootstrap time. So the
// message the edge enqueues names no host at all — it names a route — and the
// consumer resolves the origin from this record, written here, keyed by the ISR
// prefix the message carries and readable only by the consumer's own role.
//
// Whose absence is not a degradation but a silent break: an enqueued route
// whose record is missing fails to resolve on every receive, dead-letters, and
// revalidates never — while the edge's send succeeded and its sentinel re-armed.
// That is why this write fails the deploy rather than warning, and why it runs
// before the build is cut over to serving.

const (
	// originRecordVersion is the document's schema version, checked by the
	// consumer (packages/revalidator, src/origin.mts).
	originRecordVersion = 1

	// originRecordSuffix is what the consumer appends to the message's ISR
	// prefix. The suffix is also load-bearing in IAM: the consumer's read grant
	// is scoped to '*/origin.json' precisely so that no key the edge can write
	// — every one of which ends '.cache.json' — is a key it can read.
	originRecordSuffix = "/origin.json"
)

// originRecord is where one build's routes are served from: the same route id ->
// Function URL map the Deployment record hands the edge, written to the one
// place keyed by ISR prefix that the revalidator's own account can read.
type originRecord struct {
	Version      int               `json:"v"`
	FunctionURLs map[string]string `json:"functionUrls"`
}

// writeOriginRecords publishes one record per app that keeps an ISR cache, from
// the app-deploy results this deploy just produced. An app whose framework keeps
// no cache has nothing to revalidate and gets none; an app whose stack failed is
// skipped, because its promote is about to abort and its record is meaningless.
//
// Every failure here fails the deploy. There is no partial success to report: a
// build that goes live without its record has routes that enqueue and never
// revalidate, and nothing downstream would say so.
func writeOriginRecords(ctx context.Context, cfg Config, results []appDeployResult) error {
	target := uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket}
	for _, r := range results {
		if r.Err != nil || r.Record.IsrPrefix == "" {
			continue
		}
		if err := target.validate(); err != nil {
			return fmt.Errorf("publish the origin record for %s: %w", r.App, err)
		}
		body, err := json.Marshal(originRecord{Version: originRecordVersion, FunctionURLs: r.Record.FunctionURLs})
		if err != nil {
			return fmt.Errorf("encode the origin record for %s: %w", r.App, err)
		}
		key := r.Record.IsrPrefix + originRecordSuffix
		// Overwritten rather than created: a redeploy of the same build id keeps
		// its prefix, and its Function URLs are what may have moved.
		if _, err := target.up.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(target.bucket),
			Key:         aws.String(key),
			Body:        bytes.NewReader(body),
			ContentType: aws.String("application/json"),
		}); err != nil {
			return fmt.Errorf("publish the origin record %s in %s: %w; %s would go live with routes that enqueue a revalidation and never receive one", key, target.bucket, err, r.App)
		}
	}
	return nil
}
