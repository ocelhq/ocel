package gcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

type cacheStore struct {
	bucket  string
	objects *s3.Client
}

func readAdoptedCacheStore(ctx context.Context, c *clients, records keyvalue.Store, tier environment.Tier, kind edge.Kind) (cacheStore, bool, error) {
	adopted, credentials, err := readAdoptedEdge(ctx, c, records, tier, kind)
	if err != nil || adopted.CacheStore.Bucket == "" {
		return cacheStore{}, false, err
	}
	if adopted.CacheStore.Endpoint == "" || credentials.CacheStoreSecretAccessKey == "" {
		return cacheStore{}, false, notBootstrapped(tier, kind, "no cache-store endpoint or credential")
	}
	store := s3store.Store{
		Endpoint:        adopted.CacheStore.Endpoint,
		Region:          adopted.CacheStore.Region,
		AccessKeyID:     credentials.CacheStoreAccessKeyID,
		SecretAccessKey: credentials.CacheStoreSecretAccessKey,
		PathStyle:       true,
	}
	return cacheStore{bucket: adopted.CacheStore.Bucket, objects: store.Client()}, true, nil
}

func (s cacheStore) put(ctx context.Context, key, contentType string, body []byte) error {
	if _, err := s.objects.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	}); err != nil {
		return fmt.Errorf("upload %s to bucket %s: %w", key, s.bucket, err)
	}
	return nil
}

func (s cacheStore) removePrefix(ctx context.Context, prefix string) error {
	var token *string
	for {
		out, err := s.objects.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(s.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			if cacheStoreBucketGone(err) {
				return nil
			}
			return fmt.Errorf("list %s in bucket %s: %w", prefix, s.bucket, err)
		}
		if len(out.Contents) > 0 {
			ids := make([]s3types.ObjectIdentifier, len(out.Contents))
			for i, object := range out.Contents {
				ids[i] = s3types.ObjectIdentifier{Key: object.Key}
			}
			deleted, err := s.objects.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(s.bucket),
				Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
			})
			if err != nil {
				return fmt.Errorf("delete %s in bucket %s: %w", prefix, s.bucket, err)
			}
			if len(deleted.Errors) > 0 {
				first := deleted.Errors[0]
				return fmt.Errorf("delete %s in bucket %s: %d objects refused, first %s: %s",
					prefix, s.bucket, len(deleted.Errors), aws.ToString(first.Key), aws.ToString(first.Message))
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return nil
		}
		token = out.NextContinuationToken
	}
}

func cacheStoreBucketGone(err error) bool {
	var missing *s3types.NoSuchBucket
	if errors.As(err, &missing) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchBucket"
}
