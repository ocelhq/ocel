package payloads

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type ObjectStore interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type Placement struct {
	Bucket string
	Key    string
	SHA256 string
}

func (p Placement) Present() bool { return p.Bucket != "" && p.Key != "" }

func Key(prefix, digest string) string {
	return fmt.Sprintf("%s/%s.zip", prefix, digest)
}

func Place(ctx context.Context, store ObjectStore, bucket, prefix, label string, p Payload) (Placement, error) {
	if bucket == "" {
		return Placement{}, fmt.Errorf("no artifact bucket to place the %s into", label)
	}
	if store == nil {
		return Placement{}, fmt.Errorf("no artifact store configured for the %s", label)
	}
	at := Placement{Bucket: bucket, Key: Key(prefix, p.SHA256), SHA256: p.SHA256}

	head, err := store.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(at.Bucket),
		Key:          aws.String(at.Key),
		ChecksumMode: s3types.ChecksumModeEnabled,
	})
	switch {
	case err == nil:
		if aws.ToString(head.ChecksumSHA256) == p.ChecksumSHA256 {
			return at, nil
		}
	case !isNotFound(err):
		return Placement{}, fmt.Errorf("head %s payload %s/%s: %w", label, at.Bucket, at.Key, err)
	}

	if _, err := store.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(at.Bucket),
		Key:            aws.String(at.Key),
		Body:           bytes.NewReader(p.Bytes),
		ChecksumSHA256: aws.String(p.ChecksumSHA256),
	}); err != nil {
		return Placement{}, fmt.Errorf("place %s payload %s/%s: %w", label, at.Bucket, at.Key, err)
	}
	return at, nil
}

func isNotFound(err error) bool {
	var nf *s3types.NotFound
	var nsk *s3types.NoSuchKey
	return errors.As(err, &nf) || errors.As(err, &nsk)
}
