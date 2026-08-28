package ports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
)

type S3API interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

type Artifacts struct {
	S3 S3API

	Stores Stores
}

type Stores interface {
	Buckets(ctx context.Context, class kit.Class) (Buckets, error)
}

type Buckets struct {
	Functions string
	Assets    string
	Caches    []CacheBucket
}

type CacheBucket struct {
	Name string
	S3   S3API
}

func (b Buckets) Buckets(context.Context, kit.Class) (Buckets, error) { return b, nil }

func (a Artifacts) buckets(ctx context.Context, class kit.Class) (Buckets, error) {
	if class == "" {
		return Buckets{}, kit.Refuse(kit.CodeInvalid,
			"an artifact names no class, and this account keeps each class's artifacts in the bootstrap that owns them")
	}
	if a.Stores == nil {
		return Buckets{}, nil
	}
	return a.Stores.Buckets(ctx, class)
}

func (a Artifacts) bucket(ctx context.Context, class kit.Class, store string) (string, S3API, error) {
	held, err := a.buckets(ctx, class)
	if err != nil {
		return "", nil, err
	}
	name, client := "", a.S3
	switch store {
	case providerkit.StoreFunctions:
		name = held.Functions
	case providerkit.StoreAssets:
		name = held.Assets
	case providerkit.StoreCache:
		if len(held.Caches) > 1 {
			return "", nil, kit.Refuse(kit.CodeInvalid,
				"this account keeps %d cache stores, one for each edge it fronts, and an artifact names no edge", len(held.Caches))
		}
		if len(held.Caches) == 1 {
			name, client = held.Caches[0].Name, a.reach(held.Caches[0])
		}
	default:
		return "", nil, kit.Refuse(kit.CodeInvalid,
			"this provider keeps no %q store; it keeps %q, %q and %q",
			store, providerkit.StoreFunctions, providerkit.StoreAssets, providerkit.StoreCache)
	}
	if name == "" {
		return "", nil, kit.Refuse(kit.CodeNotReady,
			"this account has no %s store yet.\nRun `%s` to create it, then try again", store, providerkit.BootstrapCommand(class))
	}
	return name, client, nil
}

func (a Artifacts) reach(cache CacheBucket) S3API {
	if cache.S3 != nil {
		return cache.S3
	}
	return a.S3
}

func (a Artifacts) Put(ctx context.Context, ref providerkit.ArtifactRef, body io.Reader) error {
	bucket, client, err := a.bucket(ctx, ref.Class, ref.Bucket)
	if err != nil {
		return err
	}
	seeker, ok := body.(io.ReadSeeker)
	if !ok {
		blob, err := io.ReadAll(body)
		if err != nil {
			return fmt.Errorf("read the artifact for %s: %w", ref.Key, err)
		}
		seeker = bytes.NewReader(blob)
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(ref.Key),
		Body:   seeker,
	}); err != nil {
		return fmt.Errorf("upload %s/%s: %w", bucket, ref.Key, err)
	}
	return nil
}

func (a Artifacts) Has(ctx context.Context, ref providerkit.ArtifactRef) (bool, error) {
	bucket, client, err := a.bucket(ctx, ref.Class, ref.Bucket)
	if err != nil {
		return false, err
	}
	if _, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(ref.Key),
	}); err != nil {
		if absent(err) {
			return false, nil
		}
		return false, fmt.Errorf("look for %s/%s: %w", bucket, ref.Key, err)
	}
	return true, nil
}

func bucketGone(err error) bool {
	var missing *s3types.NoSuchBucket
	if errors.As(err, &missing) {
		return true
	}
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchBucket"
}

func absent(err error) bool {
	var missing *s3types.NotFound
	var gone *s3types.NoSuchKey
	return errors.As(err, &missing) || errors.As(err, &gone)
}

func (a Artifacts) Open(ctx context.Context, ref providerkit.ArtifactRef) (io.ReadCloser, error) {
	bucket, client, err := a.bucket(ctx, ref.Class, ref.Bucket)
	if err != nil {
		return nil, err
	}
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(ref.Key),
	})
	if err != nil {
		if absent(err) {
			return nil, kit.Refuse(kit.CodeInvalid, "no artifact at %s/%s", bucket, ref.Key)
		}
		return nil, fmt.Errorf("read %s/%s: %w", bucket, ref.Key, err)
	}
	return out.Body, nil
}

func (a Artifacts) RemovePrefix(ctx context.Context, class providerkit.Class, prefix string, report providerkit.Reporter) error {
	if prefix == "" {
		return kit.Refuse(kit.CodeInvalid, "an empty prefix names every artifact this account keeps")
	}
	held, err := a.buckets(ctx, class)
	if err != nil {
		return err
	}
	sweeps := []CacheBucket{{Name: held.Functions, S3: a.S3}, {Name: held.Assets, S3: a.S3}}
	for _, cache := range held.Caches {
		sweeps = append(sweeps, CacheBucket{Name: cache.Name, S3: a.reach(cache)})
	}
	var errs []error
	for _, sweeping := range sweeps {
		if sweeping.Name == "" {
			continue
		}
		if err := a.sweep(ctx, sweeping.S3, sweeping.Name, prefix); err != nil {
			errs = append(errs, err)
		}
	}
	if report != nil {
		report.Detail("removed " + prefix)
	}
	return errors.Join(errs...)
}

func (a Artifacts) sweep(ctx context.Context, client S3API, bucket, prefix string) error {
	var token *string
	for {
		out, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			if bucketGone(err) {
				return nil
			}
			return fmt.Errorf("list %s/%s: %w", bucket, prefix, err)
		}
		if len(out.Contents) > 0 {
			ids := make([]s3types.ObjectIdentifier, len(out.Contents))
			for i, obj := range out.Contents {
				ids[i] = s3types.ObjectIdentifier{Key: obj.Key}
			}
			if _, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &s3types.Delete{Objects: ids},
			}); err != nil {
				if bucketGone(err) {
					return nil
				}
				return fmt.Errorf("delete %s/%s: %w", bucket, prefix, err)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return nil
		}
		token = out.NextContinuationToken
	}
}

var _ providerkit.ArtifactStore = Artifacts{}
