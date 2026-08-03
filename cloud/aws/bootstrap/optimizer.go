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

// The account-global image optimizer: the Lambda every /_next/image request in a
// substrate is transformed by, and the distribution path that gets its artifact
// into the customer's own account.
//
// One optimizer per substrate, not per account. Each bootstrap stack carries its
// own AssetBucket and its own edge IAM user, and the optimizer reads originals
// and image configs out of that bucket — so a single shared function would need
// read access to both substrates' buckets, which is exactly the isolation the
// split buckets exist to provide. Production's reads only production's bucket
// and is invoked only by production's edge user; preview's likewise.

const (
	// outputImageOptimizerURL publishes the Function URL the deploy path binds
	// into every worker as OCEL_IMAGE_OPTIMIZER_URL. Absent from a stack that
	// rendered no optimizer, which leaves the worker's image origin unbound.
	outputImageOptimizerURL = "ImageOptimizerFunctionUrl"

	// optimizerAssetName is the file name the release asset is published under.
	// It matches what packages/image-optimizer/scripts/build-zip.mjs writes.
	optimizerAssetName = "image-optimizer.zip"

	// optimizerKeyPrefix is where the artifact lands in the account's artifact
	// bucket. Function artifacts there are keyed `<slug>/<function>/<hash>.zip`
	// — three segments — so no function artifact can ever land on this
	// two-segment key. The prefix is not reserved, though: destroying a project
	// slugged `ocel-image-optimizer` deletes `<slug>/` recursively and takes this
	// object with it. That costs nothing beyond a re-download, because a running
	// Lambda holds its own copy of the code and the next bootstrap re-uploads
	// what is missing — the same thing the bucket's own expiration rule does.
	optimizerKeyPrefix = "ocel-image-optimizer"

	// optimizerRuntime, optimizerArchitecture, optimizerHandler and
	// optimizerMemoryMB are what the artifact is built for and needs. arm64 is
	// the architecture build-zip.mjs cross-installs sharp's native binaries for,
	// so x86_64 would fail to load the addon at all. 1769 MB is the first size
	// AWS gives a full vCPU, and libvips is genuinely multi-threaded.
	optimizerRuntime      = "nodejs22.x"
	optimizerArchitecture = "arm64"
	optimizerHandler      = "index.handler"
	optimizerMemoryMB     = 1769

	// optimizerTimeoutSeconds must exceed the sum of the deadlines the artifact
	// enforces internally, or Lambda kills the invocation before the function can
	// answer with the error it was about to: a 7 s upstream wall clock plus a 7 s
	// libvips timeout can run back to back, and the S3 config read precedes both.
	// Overshooting costs nothing — nothing waits out this budget on a healthy
	// request — while undershooting turns every slow origin into an opaque 502.
	optimizerTimeoutSeconds = 20

	// optimizerThreadpoolSize pins libuv's pool, which is where sharp does its
	// work. The artifact also sets it in-process, but that is a no-op at libuv's
	// default of 4 and cannot raise or lower the pool once it exists — the
	// function configuration is the setting that actually binds. Peak memory
	// scales with this times sharp.concurrency().
	optimizerThreadpoolSize = 4

	// optimizerBucketEnvVar names the asset bucket to the function. The artifact
	// refuses to start without it.
	optimizerBucketEnvVar = "OCEL_IMAGE_ASSET_BUCKET"

	// optimizerDownloadCap bounds how many bytes a release download may occupy in
	// memory. The artifact is ~20 MB; anything approaching this is not it.
	optimizerDownloadCap = 64 << 20

	// optimizerDownloadTimeout bounds a whole release download, connect to last
	// byte. Bootstrap is interactive and every later step waits behind this one,
	// so a release host that accepts the connection and then never answers must
	// fail rather than hang forever.
	optimizerDownloadTimeout = 2 * time.Minute
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

// OptimizerArtifact is how bootstrap obtains the pinned optimizer zip and where
// it puts it: Source downloads the release asset, Store writes it into the
// account's own artifact bucket. A build that pins no artifact (see
// optimizerversion.go) uses neither.
type OptimizerArtifact struct {
	Source ArtifactSource
	Store  ObjectStore
}

// optimizerPin is the artifact a provider build ships: which release asset to
// download, and the digest those bytes must hash to.
type optimizerPin struct {
	version string
	sha256  string
}

// pinned reports whether this build ships an artifact at all. A half-filled pin
// counts as unpinned: a version with no digest could only be installed unverified.
func (p optimizerPin) pinned() bool { return p.version != "" && p.sha256 != "" }

// digest is the pin's sha256 in the one casing everything compares and keys on.
// The constant is hand-typed, and hex has two spellings: without this an
// uppercase pin would fail closed against a lowercase computed digest and report
// "has X, but requires X" with the two looking identical.
func (p optimizerPin) digest() string { return strings.ToLower(p.sha256) }

// checksum is the pin in the form S3 takes a ChecksumSHA256 in: base64 of the
// raw 32 digest bytes, not the hex text. Sending it on the upload is what makes
// S3 itself refuse a body that does not hash to the pin, and what makes the
// stored checksum something a later Head can verify the object against.
func (p optimizerPin) checksum() (string, error) {
	raw, err := hex.DecodeString(p.digest())
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("image optimizer pin %q is not a sha256 digest", p.sha256)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

func pinnedOptimizer() optimizerPin {
	return optimizerPin{version: ImageOptimizerArtifactVersion, sha256: ImageOptimizerArtifactSHA256}
}

// optimizerReleaseURL is where the pinned asset is published. The tag is the
// contract the release step must satisfy; nothing discovers it.
func optimizerReleaseURL(version string) string {
	return fmt.Sprintf("https://github.com/ocelhq/ocel/releases/download/image-optimizer-v%s/%s", version, optimizerAssetName)
}

// optimizerArtifactKey is content-addressed on the pinned digest, not just the
// version: a digest that moves lands at a new key, which is what makes
// CloudFormation see a code change and update the function. Keying on the
// version alone would let a re-published asset be ignored.
func optimizerArtifactKey(p optimizerPin) string {
	return fmt.Sprintf("%s/%s-%s.zip", optimizerKeyPrefix, p.version, p.digest())
}

// optimizerCode locates the uploaded artifact for CloudFormation. The zero value
// means no artifact is available, and the template then renders no optimizer at
// all rather than a function pointing at nothing.
type optimizerCode struct {
	bucket string
	key    string
}

func (c optimizerCode) present() bool { return c.bucket != "" && c.key != "" }

// ensureOptimizerArtifact makes the pinned artifact present in the account's
// artifact bucket and reports where CloudFormation should read it from. An
// unpinned build returns the zero code and no error: the caller renders no
// optimizer and the substrate degrades to the 502 it served before one existed.
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
// PutObject on the artifact bucket into arbitrary code execution behind an
// internet-reachable Function URL. So presence is not the claim — the stored
// checksum is, read back with ChecksumMode enabled and compared to the pin. An
// object with a different checksum, or with none at all (nothing verified it),
// is not trusted and is replaced by a verified download. Only a match skips the
// network, which is the common re-bootstrap of an up-to-date account.
//
// That the artifact bucket expires objects after artifactExpirationDays is
// harmless: this runs before every stack upsert, so a reaped object is fetched
// again.
func ensureOptimizerArtifact(ctx context.Context, art OptimizerArtifact, bucket string, p optimizerPin) (optimizerCode, error) {
	if !p.pinned() {
		return optimizerCode{}, nil
	}
	if bucket == "" {
		return optimizerCode{}, errors.New("no artifact bucket to upload the image optimizer into")
	}
	key := optimizerArtifactKey(p)
	if art.Store == nil || art.Source == nil {
		return optimizerCode{}, errors.New("no artifact store or source configured for the image optimizer")
	}
	checksum, err := p.checksum()
	if err != nil {
		return optimizerCode{}, err
	}

	head, err := art.Store.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket:       aws.String(bucket),
		Key:          aws.String(key),
		ChecksumMode: s3types.ChecksumModeEnabled,
	})
	switch {
	case err == nil:
		if aws.ToString(head.ChecksumSHA256) == checksum {
			return optimizerCode{bucket: bucket, key: key}, nil
		}
	case !isObjectNotFound(err):
		return optimizerCode{}, fmt.Errorf("head image optimizer artifact %s/%s: %w", bucket, key, err)
	}

	url := optimizerReleaseURL(p.version)
	data, err := art.Source.Fetch(ctx, url)
	if err != nil {
		return optimizerCode{}, fmt.Errorf("download image optimizer artifact %s: %w", url, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != p.digest() {
		return optimizerCode{}, fmt.Errorf("image optimizer artifact %s has sha256 %s, but this build requires %s; refusing to deploy it", url, got, p.digest())
	}

	if _, err := art.Store.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(bucket),
		Key:            aws.String(key),
		Body:           bytes.NewReader(data),
		ChecksumSHA256: aws.String(checksum),
	}); err != nil {
		return optimizerCode{}, fmt.Errorf("upload image optimizer artifact %s/%s: %w", bucket, key, err)
	}
	return optimizerCode{bucket: bucket, key: key}, nil
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

// optimizerReleaseClient bounds the download and refuses to leave HTTPS. A
// redirect is followed — release assets are served through one — but only to
// another https URL: the digest check makes plaintext no worse for integrity,
// while a downgrade to http would still hand the release URL, and everything a
// network position can do with it, to whatever is on the path.
func optimizerReleaseClient() *http.Client {
	return &http.Client{
		Timeout: optimizerDownloadTimeout,
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
		client = optimizerReleaseClient()
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, optimizerDownloadCap+1))
	if err != nil {
		return nil, err
	}
	if len(data) > optimizerDownloadCap {
		return nil, fmt.Errorf("asset exceeds the %d byte cap", optimizerDownloadCap)
	}
	return data, nil
}

// imageOptimizerResources renders the optimizer's execution role, function and
// Function URL, or nothing when no artifact is available.
//
// The role reads exactly the two prefixes the artifact reads — assets/ for the
// original image and image-config/ for the compiled validation config — and
// writes nothing anywhere. It is scoped to this substrate's own AssetBucket, so
// production's optimizer cannot see preview's bytes or the reverse.
//
// The function is deliberately unnamed: CloudFormation autonames it, and every
// grant against it is by !GetAtt rather than by a name something might guess or
// collide with. Nothing tags it ocel:app either — it belongs to no app, and a
// fabricated tag would both be a lie and make everything keyed off that tag
// misclassify this function.
func imageOptimizerResources(code optimizerCode) string {
	if !code.present() {
		return ""
	}
	return fmt.Sprintf(`  ImageOptimizerRole:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Effect: Allow
            Principal:
              Service: lambda.amazonaws.com
            Action: sts:AssumeRole
      ManagedPolicyArns:
        - arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
      Policies:
        - PolicyName: ocel-image-optimizer-read
          PolicyDocument:
            Version: '2012-10-17'
            Statement:
              - Effect: Allow
                Action: s3:GetObject
                Resource:
                  - !Sub '${AssetBucket.Arn}/assets/*'
                  - !Sub '${AssetBucket.Arn}/image-config/*'
  ImageOptimizer:
    Type: AWS::Lambda::Function
    Properties:
      Description: Ocel image optimizer - transforms /_next/image requests for every app in this substrate.
      Runtime: %s
      Architectures:
        - %s
      Handler: %s
      MemorySize: %d
      Timeout: %d
      Role: !GetAtt ImageOptimizerRole.Arn
      Code:
        S3Bucket: %s
        S3Key: %s
      Environment:
        Variables:
          %s: !Ref AssetBucket
          UV_THREADPOOL_SIZE: '%d'
  ImageOptimizerUrl:
    Type: AWS::Lambda::Url
    Properties:
      TargetFunctionArn: !GetAtt ImageOptimizer.Arn
      AuthType: AWS_IAM
      InvokeMode: RESPONSE_STREAM
`, optimizerRuntime, optimizerArchitecture, optimizerHandler, optimizerMemoryMB, optimizerTimeoutSeconds,
		code.bucket, code.key, optimizerBucketEnvVar, optimizerThreadpoolSize)
}

// imageOptimizerOutput publishes the Function URL, or nothing when no optimizer
// was rendered — an absent output is what leaves the worker's image origin
// unbound rather than bound to an empty string.
func imageOptimizerOutput(code optimizerCode) string {
	if !code.present() {
		return ""
	}
	return fmt.Sprintf(`  %s:
    Description: Function URL of the substrate's image optimizer, signed by the edge user.
    Value: !GetAtt ImageOptimizerUrl.FunctionUrl
`, outputImageOptimizerURL)
}

// imageOptimizerInvokeStatement renders the edge user's Allow on the optimizer,
// or nothing when none was rendered.
//
// It is a statement of its own, naming this one function, because the edge user's
// general Lambda-invoke grant is conditioned on the ocel:app tag and the
// optimizer carries no such tag — as things stood the edge could not invoke it at
// all. The two alternatives were both worse: dropping the tag condition would let
// the edge key invoke every Lambda in the customer's account, and tagging the
// optimizer ocel:app would lie about which app owns it. This way the edge gains
// exactly one new named callable target and every app function stays governed by
// the tag.
//
// An edge inside the provider's trust boundary has no edge user to carry this at
// all (edgeUserResource renders nothing for one), so such a substrate gets an
// AWS_IAM optimizer with no in-account principal allowed to invoke it. Internal
// trust is not a live path today; the edge it describes would need this same
// named grant against its own identity before it were one.
func imageOptimizerInvokeStatement(code optimizerCode) string {
	if !code.present() {
		return ""
	}
	return `              - Effect: Allow
                Action:
                  - lambda:InvokeFunctionUrl
                  - lambda:InvokeFunction
                Resource: !GetAtt ImageOptimizer.Arn
`
}
