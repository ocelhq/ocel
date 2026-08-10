package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	originRecordVersion = 1

	originRecordSuffix = "/origin.json"
)

type originRecord struct {
	Version      int               `json:"v"`
	FunctionURLs map[string]string `json:"functionUrls"`
}

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
