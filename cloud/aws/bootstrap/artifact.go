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

// How a bootstrap places an account-global Lambda's code in the customer's own
// account: download the release asset this provider build pins, verify its
// digest fail-closed, and upload it under a content-addressed key. Two
// functions are placed this way — the image optimizer (optimizer.go) and the
// tag-snapshot publisher (publisher.go) — and the discipline below is the whole
// of what keeps either of them from being arbitrary code.

const (
	// artifactDownloadCap bounds how many bytes a release download may occupy in
	// memory. The largest artifact is ~20 MB; anything approaching this is not
	// one of ours.
	artifactDownloadCap = 64 << 20

	// artifactDownloadTimeout bounds a whole release download, connect to last
	// byte. Bootstrap is interactive and every later step waits behind this one,
	// so a release host that accepts the connection and then never answers must
	// fail rather than hang forever.
	artifactDownloadTimeout = 2 * time.Minute
)

// ObjectStore is the subset of the S3 client the artifact upload needs: a
// presence check so a re-bootstrap neither re-downloads nor re-uploads, and the
// write itself.
type ObjectStore interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// ArtifactSource fetches a release asset by URL. An interface so the digest
// verification can be tested against fixture bytes with no network and no
// release.
type ArtifactSource interface {
	Fetch(ctx context.Context, url string) ([]byte, error)
}

// Artifacts is how bootstrap obtains the pinned Lambda artifacts and where it
// puts them: Source downloads the release asset, Store writes it into the
// account's own artifact bucket. A build that pins no artifact at all uses
// neither.
type Artifacts struct {
	Source ArtifactSource
	Store  ObjectStore
}

// artifactPin is one artifact a provider build ships: which release asset to
// download, and the digest those bytes must hash to.
type artifactPin struct {
	version string
	sha256  string
}

// pinned reports whether this build ships this artifact at all. A half-filled
// pin counts as unpinned: a version with no digest could only be installed
// unverified.
func (p artifactPin) pinned() bool { return p.version != "" && p.sha256 != "" }

// digest is the pin's sha256 in the one casing everything compares and keys on.
// The constant is hand-typed, and hex has two spellings: without this an
// uppercase pin would fail closed against a lowercase computed digest and report
// "has X, but requires X" with the two looking identical.
func (p artifactPin) digest() string { return strings.ToLower(p.sha256) }

// checksum is the pin in the form S3 takes a ChecksumSHA256 in: base64 of the
// raw 32 digest bytes, not the hex text. Sending it on the upload is what makes
// S3 itself refuse a body that does not hash to the pin, and what makes the
// stored checksum something a later Head can verify the object against.
func (p artifactPin) checksum(label string) (string, error) {
	raw, err := hex.DecodeString(p.digest())
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("%s pin %q is not a sha256 digest", label, p.sha256)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// artifactCode locates an uploaded artifact for CloudFormation. The zero value
// means no artifact is available, and the template then renders no function at
// all rather than one pointing at nothing.
type artifactCode struct {
	bucket string
	key    string
}

func (c artifactCode) present() bool { return c.bucket != "" && c.key != "" }

// ensureArtifact makes one pinned artifact present in the account's artifact
// bucket and reports where CloudFormation should read it from. An unpinned
// build returns the zero code and no error: the caller renders no function and
// the substrate degrades to what it did before that function existed.
//
// The digest check is fail-closed and is the only thing standing between a
// customer's account and whatever bytes that URL served. It runs on the bytes
// actually received, before anything is written to the account, and a mismatch
// aborts bootstrap rather than uploading an unverified archive — there is no
// "warn and continue" branch here by design.
//
// The upload sends the pin as S3's own ChecksumSHA256, so the digest is not
// merely checked here — S3 rejects a body that does not hash to it, and records
// the value it verified against the object.
//
// That recorded checksum is what makes skipping an already-present artifact safe.
// The key names the digest, but S3 enforces nothing about a key: anything that
// can write this bucket can put arbitrary bytes at the exact key a shipped,
// open-source CLI is compiled to ask for, and trusting mere presence would turn
// PutObject on the artifact bucket into arbitrary code execution inside the
// customer's account. So presence is not the claim — the stored checksum is,
// read back with ChecksumMode enabled and compared to the pin. An object with a
// different checksum, or with none at all (nothing verified it), is not trusted
// and is replaced by a verified download. Only a match skips the network, which
// is the common re-bootstrap of an up-to-date account.
//
// That the artifact bucket expires objects after artifactExpirationDays is
// harmless: this runs before every stack upsert, so a reaped object is fetched
// again.
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

// isObjectNotFound reports whether an S3 error is a missing-object result.
// HeadObject answers with no response body, so it cannot carry GetObject's
// NoSuchKey code: an absent key comes back as NotFound over a bare 404.
func isObjectNotFound(err error) bool {
	var nf *s3types.NotFound
	return errors.As(err, &nf)
}

// ReleaseSource downloads a release asset over HTTPS. Client may be nil, which
// uses a client of our own rather than http.DefaultClient: that one has no
// timeout, and a release host that stalls mid-body would hang bootstrap forever.
type ReleaseSource struct{ Client *http.Client }

// artifactReleaseClient bounds the download and refuses to leave HTTPS. A
// redirect is followed — release assets are served through one — but only to
// another https URL: the digest check makes plaintext no worse for integrity,
// while a downgrade to http would still hand the release URL, and everything a
// network position can do with it, to whatever is on the path.
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
	// Capped rather than read whole: this runs in the provider's process, and an
	// unbounded read of an arbitrary URL is an unbounded allocation. One byte over
	// the cap is enough to tell that it is over.
	data, err := io.ReadAll(io.LimitReader(resp.Body, artifactDownloadCap+1))
	if err != nil {
		return nil, err
	}
	if len(data) > artifactDownloadCap {
		return nil, fmt.Errorf("asset exceeds the %d byte cap", artifactDownloadCap)
	}
	return data, nil
}
