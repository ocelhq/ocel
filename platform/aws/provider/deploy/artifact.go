package deploy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const uploadConcurrency = 64

var uploadSlots = make(chan struct{}, uploadConcurrency)

func takeUploadSlot() func() {
	uploadSlots <- struct{}{}
	return func() { <-uploadSlots }
}

type ArtifactUploader = payloads.ObjectStore

func writeLenPrefixed(h io.Writer, b []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(b)))
	h.Write(size[:])
	h.Write(b)
}

func copyFileInto(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	f.Close()
	return err
}

type objectHeaders struct {
	contentType  string
	cacheControl string
}

func uploadArtifact(ctx context.Context, up ArtifactUploader, bucket, key string, headers objectHeaders, body func() ([]byte, error)) (transferred bool, err error) {
	_, err = up.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return false, nil
	}
	if !isNotFound(err) {
		return false, fmt.Errorf("head artifact %s/%s: %w", bucket, key, err)
	}
	data, err := body()
	if err != nil {
		return false, err
	}
	return true, putArtifact(ctx, up, bucket, key, headers, data)
}

func putArtifact(ctx context.Context, up ArtifactUploader, bucket, key string, headers objectHeaders, data []byte) error {
	in := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	}
	if headers.contentType != "" {
		in.ContentType = aws.String(headers.contentType)
	}
	if headers.cacheControl != "" {
		in.CacheControl = aws.String(headers.cacheControl)
	}
	if _, err := up.PutObject(ctx, in); err != nil {
		return fmt.Errorf("upload artifact %s/%s: %w", bucket, key, err)
	}
	return nil
}

func tracedUpload(ctx context.Context, up ArtifactUploader, bucket, key string, headers objectHeaders, body func() ([]byte, error), stats *uploadBatchStats) error {
	start := time.Now()
	var size int64
	transferred, err := uploadArtifact(ctx, up, bucket, key, headers, func() ([]byte, error) {
		data, err := body()
		if err == nil {
			size = int64(len(data))
		}
		return data, err
	})
	if stats != nil {
		stats.record(uploadOutcome{Bytes: size, Start: start, End: time.Now(), Failed: err != nil, Err: err, Transferred: transferred})
	}
	return err
}

func tracedPut(ctx context.Context, up ArtifactUploader, bucket, key string, headers objectHeaders, data []byte, stats *uploadBatchStats) error {
	start := time.Now()
	err := putArtifact(ctx, up, bucket, key, headers, data)
	if stats != nil {
		stats.record(uploadOutcome{Bytes: int64(len(data)), Start: start, End: time.Now(), Failed: err != nil, Err: err, Transferred: true})
	}
	return err
}

func isNotFound(err error) bool {
	var nf *s3types.NotFound
	var nsk *s3types.NoSuchKey
	return errors.As(err, &nf) || errors.As(err, &nsk)
}

type artifactRef struct {
	Bucket string
	Key    string
}
