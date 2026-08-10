package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	artifactDownloadCap = 64 << 20

	artifactDownloadTimeout = 2 * time.Minute
)

type ObjectStore interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type ArtifactSource interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
}

type Artifacts struct {
	Source ArtifactSource
	Store  ObjectStore
}

type artifactPin struct {
	version string
	sha256  string
}

func (p artifactPin) pinned() bool { return p.version != "" && p.sha256 != "" }

func (p artifactPin) digest() string { return strings.ToLower(p.sha256) }

func (p artifactPin) checksum(label string) (string, error) {
	raw, err := hex.DecodeString(p.digest())
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("%s pin %q is not a sha256 digest", label, p.sha256)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

type artifactCode struct {
	bucket string
	key    string
}

func (c artifactCode) present() bool { return c.bucket != "" && c.key != "" }

func ensureArtifact(ctx context.Context, art Artifacts, bucket, key, url, label string, p artifactPin) (artifactCode, error) {
	if !p.pinned() {
		return artifactCode{}, nil
	}
	if bucket == "" {
		return artifactCode{}, fmt.Errorf("no artifact bucket to upload the %s into", label)
	}
	if art.Store == nil || art.Source == nil {
		return artifactCode{}, fmt.Errorf("no artifact store or source configured for the %s", label)
	}
	checksum, err := p.checksum(label)
	if err != nil {
		return artifactCode{}, err
	}

	head, err := art.Store.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(bucket),
		Key:          aws.String(key),
		ChecksumMode: s3types.ChecksumModeEnabled,
	})
	switch {
	case err == nil:
		if aws.ToString(head.ChecksumSHA256) == checksum {
			return artifactCode{bucket: bucket, key: key}, nil
		}
	case !isObjectNotFound(err):
		return artifactCode{}, fmt.Errorf("head %s artifact %s/%s: %w", label, bucket, key, err)
	}

	data, err := art.Source.Fetch(ctx, url)
	if err != nil {
		return artifactCode{}, fmt.Errorf("download %s artifact %s: %w", label, url, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != p.digest() {
		return artifactCode{}, fmt.Errorf("%s artifact %s has sha256 %s, but this build requires %s; refusing to deploy it", label, url, got, p.digest())
	}

	if _, err := art.Store.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(bucket),
		Key:            aws.String(key),
		Body:           bytes.NewReader(data),
		ChecksumSHA256: aws.String(checksum),
	}); err != nil {
		return artifactCode{}, fmt.Errorf("upload %s artifact %s/%s: %w", label, bucket, key, err)
	}
	return artifactCode{bucket: bucket, key: key}, nil
}

func isObjectNotFound(err error) bool {
	var nf *s3types.NotFound
	return errors.As(err, &nf)
}

type ReleaseSource struct{ Client *http.Client }

func artifactReleaseClient() *http.Client {
	return &http.Client{
		Timeout: artifactDownloadTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing a redirect to %s: the release asset must stay on https", req.URL.Scheme)
			}
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

func (s ReleaseSource) Fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	client := s.Client
	if client == nil {
		client = artifactReleaseClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, artifactDownloadCap+1))
	if err != nil {
		return nil, err
	}
	if len(data) > artifactDownloadCap {
		return nil, fmt.Errorf("asset exceeds the %d byte cap", artifactDownloadCap)
	}
	return data, nil
}
