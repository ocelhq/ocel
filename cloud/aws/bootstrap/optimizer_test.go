package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/cloud/edge"
)

// The digest verification is exercised entirely against fixture bytes. Nothing
// here downloads anything, and nothing here knows the real release's digest —
// which is the point: the guarantee under test is "these bytes hash to what this
// build pinned", and a fixture proves that as well as a release does.
var fixtureArtifact = []byte("PK\x03\x04 pretend this is a Lambda zip")

func fixtureDigest() string {
	sum := sha256.Sum256(fixtureArtifact)
	return hex.EncodeToString(sum[:])
}

func fixturePin() artifactPin {
	return artifactPin{version: "1.2.3", sha256: fixtureDigest()}
}

// fixtureOptimizerCode is an already-uploaded artifact, for the template tests:
// they assert what CloudFormation is asked to provision, not how the zip got
// there.
func fixtureOptimizerCode() artifactCode {
	return artifactCode{bucket: "ocel-artifacts-test", key: optimizerArtifactKey(fixturePin())}
}

// fakeArtifactStore is an S3 bucket holding objects by key, recording every
// write so a test can prove that a refused artifact was never uploaded.
//
// It models the part of S3 the supply-chain guarantee rests on: a key says
// nothing about the bytes under it, but a checksum sent with a PutObject is
// verified by S3 and then recorded against the object, so a later HeadObject can
// read it back. `checksums` is therefore separate from `objects` — an object put
// without a checksum has none to report, exactly like a pre-checksum upload or
// one written by somebody else.
type fakeArtifactStore struct {
	objects   map[string][]byte
	checksums map[string]string
	// headModes records the ChecksumMode of every HeadObject: without ENABLED, S3
	// does not return the stored checksum at all and there is nothing to verify.
	headModes []s3types.ChecksumMode
	// putChecksums records the ChecksumSHA256 of every PutObject, so a test can
	// prove S3 was actually told what the body must hash to.
	putChecksums []string
	puts         int
}

func newFakeArtifactStore() *fakeArtifactStore {
	return &fakeArtifactStore{objects: map[string][]byte{}, checksums: map[string]string{}}
}

// put writes bytes the way something other than this bootstrap would have: no
// verified checksum recorded against them, whatever key they land on.
func (f *fakeArtifactStore) put(key string, body []byte) {
	f.objects[key] = body
}

// putVerified writes bytes with the checksum S3 would have verified and recorded
// for them.
func (f *fakeArtifactStore) putVerified(key string, body []byte) {
	sum := sha256.Sum256(body)
	f.objects[key] = body
	f.checksums[key] = base64.StdEncoding.EncodeToString(sum[:])
}

func (f *fakeArtifactStore) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.headModes = append(f.headModes, in.ChecksumMode)
	if _, ok := f.objects[aws.ToString(in.Key)]; !ok {
		return nil, &s3types.NotFound{}
	}
	out := &s3.HeadObjectOutput{}
	if in.ChecksumMode == s3types.ChecksumModeEnabled {
		if sum, ok := f.checksums[aws.ToString(in.Key)]; ok {
			out.ChecksumSHA256 = aws.String(sum)
		}
	}
	return out, nil
}

func (f *fakeArtifactStore) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.puts++
	f.putChecksums = append(f.putChecksums, aws.ToString(in.ChecksumSHA256))
	body := make([]byte, 0)
	buf := make([]byte, 512)
	for {
		n, err := in.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	key := aws.ToString(in.Key)
	// S3 rejects a body that does not hash to the checksum it was given, and
	// records the checksum only for a body that did.
	if declared := aws.ToString(in.ChecksumSHA256); declared != "" {
		sum := sha256.Sum256(body)
		if base64.StdEncoding.EncodeToString(sum[:]) != declared {
			return nil, errors.New("BadDigest: the sha256 you specified did not match what we received")
		}
		f.checksums[key] = declared
	}
	f.objects[key] = body
	return &s3.PutObjectOutput{}, nil
}

// fakeArtifactSource serves fixture bytes for whatever URL is asked for,
// recording the URLs so a test can prove which release asset was fetched.
type fakeArtifactSource struct {
	body []byte
	err  error
	urls []string
}

func (f *fakeArtifactSource) Fetch(_ context.Context, url string) ([]byte, error) {
	f.urls = append(f.urls, url)
	return f.body, f.err
}

func fixtureArtifactDeps(body []byte) (Artifacts, *fakeArtifactStore, *fakeArtifactSource) {
	store, source := newFakeArtifactStore(), &fakeArtifactSource{body: body}
	return Artifacts{Source: source, Store: store}, store, source
}

// preloadedArtifact is an account that already holds whatever artifact this build
// pins, under the checksum S3 would have verified for it. Tests of the bootstrap
// steps *around* the optimizer use it so they neither download nor verify
// anything and behave identically before and after a release is cut. The recorded
// checksum is the pin's rather than the fixture's, because what the skip turns on
// is the stored checksum matching the pin — the bytes stand in for a release
// nobody has cut.
func preloadedArtifact() Artifacts {
	store := newFakeArtifactStore()
	pin := pinnedOptimizer()
	key := optimizerArtifactKey(pin)
	store.put(key, fixtureArtifact)
	if sum, err := pin.checksum(optimizerLabel); err == nil {
		store.checksums[key] = sum
	}
	return Artifacts{Source: &fakeArtifactSource{}, Store: store}
}

// TestEnsureOptimizerArtifact_RefusesADigestMismatch is the whole point of
// pinning a digest in a reviewed source file: bytes that are not the reviewed
// artifact must never reach the customer's account, and bootstrap must stop
// rather than carry on with something unverified.
func TestEnsureOptimizerArtifact_RefusesADigestMismatch(t *testing.T) {
	art, store, source := fixtureArtifactDeps([]byte("not the artifact anyone reviewed"))

	code, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test", fixturePin())
	if err == nil {
		t.Fatal("a mismatched artifact was accepted; it must refuse to deploy")
	}
	if code.present() {
		t.Errorf("a refused artifact still produced code %+v", code)
	}
	if store.puts != 0 {
		t.Errorf("a refused artifact was uploaded anyway (%d puts)", store.puts)
	}
	if len(source.urls) != 1 {
		t.Errorf("fetched %v, want exactly the pinned release asset once", source.urls)
	}
	// The message has to name both digests: an operator seeing this needs to know
	// whether the release moved or the download was tampered with.
	if !strings.Contains(err.Error(), fixturePin().sha256) {
		t.Errorf("error does not name the required digest: %v", err)
	}
}

// TestEnsureOptimizerArtifact_UploadsAVerifiedArtifact proves the happy path
// puts the exact reviewed bytes into the account's own bucket, at a key
// content-addressed on the digest so a moved pin lands somewhere new.
func TestEnsureOptimizerArtifact_UploadsAVerifiedArtifact(t *testing.T) {
	art, store, source := fixtureArtifactDeps(fixtureArtifact)

	code, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test", fixturePin())
	if err != nil {
		t.Fatalf("ensureOptimizerArtifact: %v", err)
	}
	if code.bucket != "ocel-artifacts-test" {
		t.Errorf("uploaded into %q, want the account's own artifact bucket", code.bucket)
	}
	if !strings.Contains(code.key, fixtureDigest()) {
		t.Errorf("key %q is not content-addressed on the pinned digest", code.key)
	}
	if got := string(store.objects[code.key]); got != string(fixtureArtifact) {
		t.Errorf("uploaded %q, want the verified bytes verbatim", got)
	}
	want := optimizerReleaseURL("1.2.3")
	if len(source.urls) != 1 || source.urls[0] != want {
		t.Errorf("fetched %v, want [%s]", source.urls, want)
	}
}

// TestEnsureOptimizerArtifact_UploadSendsTheDigestToS3 is what makes the skip
// below safe. S3 enforces nothing about a key, so the digest has to be handed to
// S3 itself: it then refuses a body that does not hash to it, and records the
// value it verified against the object for a later Head to read back.
func TestEnsureOptimizerArtifact_UploadSendsTheDigestToS3(t *testing.T) {
	art, store, _ := fixtureArtifactDeps(fixtureArtifact)

	if _, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test", fixturePin()); err != nil {
		t.Fatalf("ensureOptimizerArtifact: %v", err)
	}
	want, err := fixturePin().checksum(optimizerLabel)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if len(store.putChecksums) != 1 || store.putChecksums[0] != want {
		t.Errorf("uploaded with checksums %v, want [%s] — base64 of the raw digest, not the hex", store.putChecksums, want)
	}
	// The hex spelling is what an unchecked implementation would send, and S3
	// would record it verbatim: it must not appear.
	for _, got := range store.putChecksums {
		if got == fixtureDigest() {
			t.Error("sent the hex digest as ChecksumSHA256; S3 wants base64 of the raw bytes")
		}
	}
}

// TestEnsureOptimizerArtifact_SkipsAnArtifactS3VerifiedAgainstThePin proves a
// re-bootstrap of an up-to-date account needs no release at all — and proves what
// the skip actually turns on. Presence is not enough: the object is trusted
// because S3 holds a checksum it verified, this Head asked for it, and it equals
// the pin.
func TestEnsureOptimizerArtifact_SkipsAnArtifactS3VerifiedAgainstThePin(t *testing.T) {
	art, store, source := fixtureArtifactDeps(fixtureArtifact)
	store.putVerified(optimizerArtifactKey(fixturePin()), fixtureArtifact)

	code, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test", fixturePin())
	if err != nil {
		t.Fatalf("ensureOptimizerArtifact: %v", err)
	}
	if !code.present() {
		t.Error("an artifact already in the bucket was not used")
	}
	if len(source.urls) != 0 {
		t.Errorf("downloaded %v for an artifact already present", source.urls)
	}
	if store.puts != 0 {
		t.Errorf("re-uploaded an artifact already present (%d puts)", store.puts)
	}
	// Without ENABLED there is no checksum in the response at all, so the
	// comparison would be against "" and every object would look untrusted.
	if len(store.headModes) != 1 || store.headModes[0] != s3types.ChecksumModeEnabled {
		t.Errorf("head checksum modes %v, want [%s]", store.headModes, s3types.ChecksumModeEnabled)
	}
}

// TestEnsureOptimizerArtifact_DistrustsBytesAtThePinnedKey is the supply-chain
// hole this guards. Both constants ship in an open-source CLI, so the exact key
// every customer's bucket will use is public: anything that can write that bucket
// can pre-place a file there. Presence alone must therefore never be accepted —
// an object S3 holds no matching verified checksum for is replaced by a verified
// download, and if that download does not match the pin either, bootstrap stops
// rather than pointing a Lambda at whatever was sitting at the key.
func TestEnsureOptimizerArtifact_DistrustsBytesAtThePinnedKey(t *testing.T) {
	key := optimizerArtifactKey(fixturePin())
	planted := []byte("MZ\x90\x00 an executable nobody reviewed")

	for _, tc := range []struct {
		name string
		seed func(*fakeArtifactStore)
	}{
		// Written by something other than a verifying upload: no checksum at all.
		{"no stored checksum", func(s *fakeArtifactStore) { s.put(key, planted) }},
		// Written with its own checksum, which S3 happily verified — against the
		// planted bytes, not against the pin.
		{"a checksum of the wrong bytes", func(s *fakeArtifactStore) { s.putVerified(key, planted) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			art, store, source := fixtureArtifactDeps(fixtureArtifact)
			tc.seed(store)

			code, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test", fixturePin())
			if err != nil {
				t.Fatalf("ensureOptimizerArtifact: %v", err)
			}
			if len(source.urls) != 1 {
				t.Errorf("fetched %v, want the pinned asset — the planted object was trusted", source.urls)
			}
			if store.puts != 1 {
				t.Errorf("uploaded %d times, want the planted object overwritten once", store.puts)
			}
			if got := string(store.objects[code.key]); got != string(fixtureArtifact) {
				t.Errorf("the account now holds %q, want the verified artifact", got)
			}
		})
	}

	// And with nothing verifiable to replace it with, bootstrap refuses outright
	// rather than falling back on what is already there.
	t.Run("and refuses when the release cannot be verified either", func(t *testing.T) {
		art, store, _ := fixtureArtifactDeps([]byte("tampered release too"))
		store.putVerified(key, planted)

		if _, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test", fixturePin()); err == nil {
			t.Fatal("planted bytes were accepted when the release could not be verified")
		}
		if got := string(store.objects[key]); got != string(planted) {
			t.Errorf("the store was written to anyway: %q", got)
		}
	})
}

// TestEnsureOptimizerArtifact_RefusesAPinThatIsNotADigest keeps the checksum
// path fail-closed on a typo. A pin that cannot be decoded to 32 bytes has no
// base64 form to hand S3, so nothing is downloaded or uploaded under it.
func TestEnsureOptimizerArtifact_RefusesAPinThatIsNotADigest(t *testing.T) {
	art, store, source := fixtureArtifactDeps(fixtureArtifact)

	_, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test",
		artifactPin{version: "1.2.3", sha256: "not-a-digest"})
	if err == nil {
		t.Fatal("a pin that is not a sha256 was accepted")
	}
	if len(source.urls) != 0 || store.puts != 0 {
		t.Errorf("an undecodable pin touched the network or the bucket: %v, %d puts", source.urls, store.puts)
	}
}

// TestOptimizerPin_DigestIsCaseInsensitive covers a hand-typed constant's other
// spelling. Hex has two, sha256.Sum256 renders one, and an uppercase pin
// compared byte-for-byte would fail closed with a message reporting the same
// digest twice.
func TestOptimizerPin_DigestIsCaseInsensitive(t *testing.T) {
	upper := artifactPin{version: "1.2.3", sha256: strings.ToUpper(fixtureDigest())}
	if got, want := optimizerArtifactKey(upper), optimizerArtifactKey(fixturePin()); got != want {
		t.Errorf("an uppercase pin keys at %q, want %q", got, want)
	}

	art, store, _ := fixtureArtifactDeps(fixtureArtifact)
	code, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test", upper)
	if err != nil {
		t.Fatalf("an uppercase pin refused its own artifact: %v", err)
	}
	if got := string(store.objects[code.key]); got != string(fixtureArtifact) {
		t.Errorf("uploaded %q, want the verified bytes", got)
	}
}

// TestEnsureOptimizerArtifact_SurfacesADownloadFailure proves an unreachable
// release stops bootstrap rather than quietly leaving the account without an
// optimizer it was told to install.
func TestEnsureOptimizerArtifact_SurfacesADownloadFailure(t *testing.T) {
	art, store, source := fixtureArtifactDeps(nil)
	source.err = errors.New("connection reset")

	if _, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test", fixturePin()); err == nil {
		t.Fatal("a failed download was not reported")
	}
	if store.puts != 0 {
		t.Errorf("a failed download still uploaded something (%d puts)", store.puts)
	}
}

// TestReleaseSource_DoesNotUseTheDefaultClient covers the two things
// http.DefaultClient gets wrong for a download that blocks an interactive
// bootstrap: it has no timeout at all, so a release host that accepts the
// connection and then stalls hangs bootstrap forever, and it follows a redirect
// off https without comment.
func TestReleaseSource_DoesNotUseTheDefaultClient(t *testing.T) {
	client := artifactReleaseClient()
	if client.Timeout == 0 {
		t.Error("the release client has no timeout; a stalled host would hang bootstrap")
	}

	// httptest serves plaintext, so redirecting between two of its handlers is
	// exactly the downgrade the guard exists to refuse.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixtureArtifact)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	_, err := ReleaseSource{}.Fetch(context.Background(), redirector.URL)
	if err == nil {
		t.Fatal("followed a redirect off https")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("error does not say why the redirect was refused: %v", err)
	}
}

// TestOptimizerPin_HalfPinnedCountsAsUnpinned proves a version with no digest
// installs nothing: the only way to ship an unverified artifact would be to
// treat a missing digest as "skip the check", and that branch must not exist.
func TestOptimizerPin_HalfPinnedCountsAsUnpinned(t *testing.T) {
	for _, p := range []artifactPin{
		{},
		{version: "1.2.3"},
		{sha256: fixtureDigest()},
	} {
		if p.pinned() {
			t.Errorf("pin %+v counts as pinned", p)
		}
		art, store, source := fixtureArtifactDeps(fixtureArtifact)
		code, err := ensureOptimizerArtifact(context.Background(), art, "ocel-artifacts-test", p)
		if err != nil {
			t.Errorf("pin %+v: %v", p, err)
		}
		if code.present() {
			t.Errorf("pin %+v produced code %+v", p, code)
		}
		if len(source.urls) != 0 || store.puts != 0 {
			t.Errorf("pin %+v touched the network or the bucket", p)
		}
	}
}

// optimizerTemplate is the subset of the rendered template the optimizer tests
// read: the function's compute settings, its environment, its role's grants, the
// Function URL's auth and invoke mode, and the edge user's statements.
type optimizerTemplate struct {
	Resources map[string]struct {
		Type       string `yaml:"Type"`
		Properties struct {
			Runtime       string   `yaml:"Runtime"`
			Architectures []string `yaml:"Architectures"`
			Handler       string   `yaml:"Handler"`
			MemorySize    int      `yaml:"MemorySize"`
			Timeout       int      `yaml:"Timeout"`
			Code          struct {
				S3Bucket string `yaml:"S3Bucket"`
				S3Key    string `yaml:"S3Key"`
			} `yaml:"Code"`
			Environment struct {
				Variables map[string]string `yaml:"Variables"`
			} `yaml:"Environment"`
			AuthType   string `yaml:"AuthType"`
			InvokeMode string `yaml:"InvokeMode"`
			Policies   []struct {
				PolicyName     string `yaml:"PolicyName"`
				PolicyDocument struct {
					Statement []struct {
						Effect    string      `yaml:"Effect"`
						Action    interface{} `yaml:"Action"`
						Resource  interface{} `yaml:"Resource"`
						Condition interface{} `yaml:"Condition"`
					} `yaml:"Statement"`
				} `yaml:"PolicyDocument"`
			} `yaml:"Policies"`
		} `yaml:"Properties"`
	} `yaml:"Resources"`
	Outputs map[string]struct {
		Value string `yaml:"Value"`
	} `yaml:"Outputs"`
}

func parseOptimizerTemplate(t *testing.T, template string) optimizerTemplate {
	t.Helper()
	var tmpl optimizerTemplate
	if err := yaml.Unmarshal([]byte(template), &tmpl); err != nil {
		t.Fatalf("template is not valid YAML: %v", err)
	}
	return tmpl
}

// TestStackTemplate_OptimizerComputeMatchesTheArtifact proves the function is
// created with what the artifact actually needs. Every one of these is a hard
// failure if it drifts: x86_64 cannot load the cross-installed arm64 sharp, the
// wrong handler name never runs, and a timeout under the artifact's own deadlines
// turns a slow upstream into an opaque Lambda kill.
func TestStackTemplate_OptimizerComputeMatchesTheArtifact(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureOptimizerCode(), RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureOptimizerCode(), RequiredBootstrapVersion)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := parseOptimizerTemplate(t, tc.template)
			fn, ok := tmpl.Resources["ImageOptimizer"]
			if !ok {
				t.Fatalf("no ImageOptimizer function in the %s template", tc.name)
			}
			if fn.Type != "AWS::Lambda::Function" {
				t.Errorf("ImageOptimizer is a %s", fn.Type)
			}
			p := fn.Properties
			if p.Runtime != "nodejs22.x" {
				t.Errorf("Runtime = %q, want nodejs22.x", p.Runtime)
			}
			if len(p.Architectures) != 1 || p.Architectures[0] != "arm64" {
				t.Errorf("Architectures = %v, want [arm64] — the artifact ships arm64 sharp binaries", p.Architectures)
			}
			if p.Handler != "index.handler" {
				t.Errorf("Handler = %q, want index.handler", p.Handler)
			}
			if p.MemorySize != 1769 {
				t.Errorf("MemorySize = %d, want 1769 (a full vCPU)", p.MemorySize)
			}
			// 7 s upstream + 7 s libvips can run back to back, and the config read
			// precedes both.
			if p.Timeout <= 14 {
				t.Errorf("Timeout = %d, which is under the artifact's own summed deadlines", p.Timeout)
			}
			if p.Code.S3Bucket != fixtureOptimizerCode().bucket || p.Code.S3Key != fixtureOptimizerCode().key {
				t.Errorf("Code = %s/%s, want the artifact uploaded into this account", p.Code.S3Bucket, p.Code.S3Key)
			}
			// The YAML parser drops the shorthand tag, so an intrinsic reads back as
			// its argument alone: `!Ref AssetBucket` as "AssetBucket". The raw check
			// below is what holds it to being a reference rather than a literal.
			if got := p.Environment.Variables[optimizerBucketEnvVar]; got != "AssetBucket" {
				t.Errorf("%s = %q, want this substrate's own asset bucket", optimizerBucketEnvVar, got)
			}
			if !strings.Contains(tc.template, optimizerBucketEnvVar+": !Ref AssetBucket") {
				t.Errorf("%s is not a reference to this stack's own AssetBucket", optimizerBucketEnvVar)
			}
			// The artifact's in-process pin is a no-op at libuv's default, so the
			// function configuration is the setting that actually binds.
			if got := p.Environment.Variables["UV_THREADPOOL_SIZE"]; got != "4" {
				t.Errorf("UV_THREADPOOL_SIZE = %q, want 4", got)
			}

			url, ok := tmpl.Resources["ImageOptimizerUrl"]
			if !ok {
				t.Fatal("no Function URL for the optimizer")
			}
			if url.Type != "AWS::Lambda::Url" {
				t.Errorf("ImageOptimizerUrl is a %s", url.Type)
			}
			if url.Properties.AuthType != "AWS_IAM" {
				t.Errorf("AuthType = %q, want AWS_IAM — an unauthenticated optimizer is an open SSRF proxy", url.Properties.AuthType)
			}
			if url.Properties.InvokeMode != "RESPONSE_STREAM" {
				t.Errorf("InvokeMode = %q, want RESPONSE_STREAM", url.Properties.InvokeMode)
			}

			out, ok := tmpl.Outputs[outputImageOptimizerURL]
			if !ok {
				t.Fatalf("the %s template publishes no %s output", tc.name, outputImageOptimizerURL)
			}
			if !strings.Contains(out.Value, "ImageOptimizerUrl") {
				t.Errorf("%s = %q, want the Function URL attribute", outputImageOptimizerURL, out.Value)
			}
		})
	}
}

// TestStackTemplate_OptimizerReadsOnlyItsOwnSubstrateAsset is the isolation the
// per-substrate optimizer exists for: it may read the two prefixes it needs from
// the asset bucket its own stack owns, and it may write nothing anywhere. A grant
// naming any other bucket would hand production's bytes to preview or the reverse.
func TestStackTemplate_OptimizerReadsOnlyItsOwnSubstrateAsset(t *testing.T) {
	tmpl := parseOptimizerTemplate(t, stackTemplate(edge.TrustExternal, fixtureOptimizerCode(), RequiredBootstrapVersion))
	role, ok := tmpl.Resources["ImageOptimizerRole"]
	if !ok {
		t.Fatal("no execution role for the optimizer")
	}
	if role.Type != "AWS::IAM::Role" {
		t.Errorf("ImageOptimizerRole is a %s", role.Type)
	}
	if len(role.Properties.Policies) != 1 {
		t.Fatalf("role carries %d inline policies, want exactly 1", len(role.Properties.Policies))
	}
	statements := role.Properties.Policies[0].PolicyDocument.Statement
	if len(statements) != 1 {
		t.Fatalf("role grants %d statements, want exactly 1", len(statements))
	}
	if got, want := statements[0].Action, "s3:GetObject"; got != want {
		t.Errorf("Action = %v, want only %s — the optimizer writes nothing", got, want)
	}
	resources, ok := statements[0].Resource.([]interface{})
	if !ok || len(resources) != 2 {
		t.Fatalf("Resource = %v, want exactly the two prefixes it reads", statements[0].Resource)
	}
	for _, want := range []string{"${AssetBucket.Arn}/assets/*", "${AssetBucket.Arn}/image-config/*"} {
		found := false
		for _, r := range resources {
			if s, ok := r.(string); ok && strings.Contains(s, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("Resource %v does not grant %s", resources, want)
		}
	}
}

// TestEdgeUser_OptimizerInvokeIsItsOwnNamedStatement is the constraint that made
// this statement necessary at all. The edge user's general Lambda-invoke grant is
// conditioned on the ocel:app tag; the optimizer carries no such tag, so without
// a statement of its own the edge could not invoke it. The two shortcuts are both
// refused here: the tag condition must still be present on the general statement
// (dropping it would let the edge key invoke every Lambda in the account), and
// the optimizer's own statement must name the function rather than a wildcard.
func TestEdgeUser_OptimizerInvokeIsItsOwnNamedStatement(t *testing.T) {
	template := stackTemplate(edge.TrustExternal, fixtureOptimizerCode(), RequiredBootstrapVersion)
	tmpl := parseOptimizerTemplate(t, template)
	user, ok := tmpl.Resources["EdgeUser"]
	if !ok {
		t.Fatal("no edge user in the template")
	}
	statements := user.Properties.Policies[0].PolicyDocument.Statement

	var tagged, named int
	for _, s := range statements {
		actions, ok := s.Action.([]interface{})
		if !ok || len(actions) == 0 || !strings.HasPrefix(actions[0].(string), "lambda:") {
			continue
		}
		res, _ := s.Resource.(string)
		switch {
		case s.Condition != nil:
			tagged++
			if !strings.Contains(res, "function:*") {
				t.Errorf("the tag-conditioned statement no longer covers app functions: %v", res)
			}
		default:
			named++
			// The parser drops the !GetAtt tag, leaving its argument; the raw check
			// below holds it to being the attribute reference and not a literal.
			if res != "ImageOptimizer.Arn" {
				t.Errorf("the optimizer's invoke grant names %q, want the function's own ARN", res)
			}
		}
	}
	if tagged != 1 {
		t.Errorf("found %d tag-conditioned Lambda statements, want exactly 1 left intact", tagged)
	}
	if named != 1 {
		t.Errorf("found %d unconditioned Lambda statements, want exactly 1 naming the optimizer", named)
	}
	if !strings.Contains(template, "Resource: !GetAtt ImageOptimizer.Arn") {
		t.Error("the optimizer's invoke grant is not a reference to the function's own ARN")
	}
}

// TestStackTemplate_NoArtifactRendersNoOptimizer is the degradation path a build
// pinning no artifact takes, and the shape the first pass of a first bootstrap
// submits. Nothing may reference a function that does not exist — an !GetAtt on a
// missing resource fails the whole stack — and no Function URL output may appear,
// because an empty one bound onto a worker would be worse than none.
func TestStackTemplate_NoArtifactRendersNoOptimizer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, artifactCode{}, RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, artifactCode{}, RequiredBootstrapVersion)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Contains(tc.template, "ImageOptimizer") {
				t.Errorf("a template with no artifact still names the optimizer:\n%s", tc.template)
			}
			tmpl := parseOptimizerTemplate(t, tc.template)
			for name, r := range tmpl.Resources {
				if r.Type == "AWS::Lambda::Function" || r.Type == "AWS::Lambda::Url" {
					t.Errorf("resource %s (%s) was rendered without an artifact", name, r.Type)
				}
			}
			if _, ok := tmpl.Outputs[outputImageOptimizerURL]; ok {
				t.Errorf("a template with no artifact still publishes %s", outputImageOptimizerURL)
			}
			// The edge user must still be whole, minus that one statement.
			if !strings.Contains(tc.template, "aws:ResourceTag/ocel:app") {
				t.Error("the edge user lost its tag-conditioned Lambda grant")
			}
		})
	}
}

// TestRun_FirstBootstrapSeedsTheBucketThenPlacesTheArtifact is the ordering the
// whole distribution path hangs on: the artifact bucket is created by this very
// stack, so on a first bootstrap it cannot be uploaded into until one pass has
// settled. The account must still end up with an optimizer.
func TestRun_FirstBootstrapSeedsTheBucketThenPlacesTheArtifact(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	art, store, source := fixtureArtifactDeps(fixtureArtifact)
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := run(context.Background(), cfn, ssmc, iamc, ed, art, fixturePin(), productionSubstrate(), nil, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if cfn.creates != 1 || cfn.updates != 1 {
		t.Errorf("settled the stack %d creates + %d updates, want one seeding create then one update", cfn.creates, cfn.updates)
	}
	if len(source.urls) != 1 {
		t.Errorf("fetched %v, want the pinned asset exactly once", source.urls)
	}
	if store.puts != 1 {
		t.Errorf("uploaded %d artifacts, want 1", store.puts)
	}
	final := cfn.templates[StackName]
	if !strings.Contains(final, "AWS::Lambda::Url") {
		t.Errorf("the settled template carries no optimizer:\n%s", final)
	}
	if !strings.Contains(final, optimizerArtifactKey(fixturePin())) {
		t.Error("the settled template does not point at the uploaded artifact")
	}
}

// TestRun_RefusedArtifactLeavesAnExistingAccountAlone proves fail-closed reaches
// all the way out to an account that already has the artifact bucket: the
// artifact step runs before anything is upserted, so bytes that do not match the
// pin abort bootstrap with the account exactly as it was rather than
// half-upgraded around a rejected artifact.
func TestRun_RefusedArtifactLeavesAnExistingAccountAlone(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	cfn.templates[StackName] = "existing"
	art, _, _ := fixtureArtifactDeps([]byte("tampered"))
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	err := run(context.Background(), cfn, ssmc, iamc, ed, art, fixturePin(), productionSubstrate(), nil, nil)
	if err == nil {
		t.Fatal("bootstrap accepted a mismatched artifact")
	}
	if cfn.templates[StackName] != "existing" {
		t.Errorf("the stack was updated anyway:\n%s", cfn.templates[StackName])
	}
	if cfn.creates != 0 || cfn.updates != 0 {
		t.Errorf("settled the stack %d creates + %d updates, want none", cfn.creates, cfn.updates)
	}
}

// TestRun_RefusedArtifactOnAVirginAccountFailsTheGate is the path the two-settle
// design exists for, and the one where "leaves the account alone" is impossible:
// a first bootstrap's seeding pass is what creates the artifact bucket, so it
// necessarily runs before the artifact can be obtained, and a refusal leaves a
// created stack behind. What must not happen is that stack satisfying the
// compatibility gate — an account stamped with the required version and no
// optimizer deploys happily and 502s every image, permanently in browsers, with
// nothing pointing at the cause.
func TestRun_RefusedArtifactOnAVirginAccountFailsTheGate(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	art, _, _ := fixtureArtifactDeps([]byte("tampered"))
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	err := run(context.Background(), cfn, ssmc, iamc, ed, art, fixturePin(), productionSubstrate(), nil, nil)
	if err == nil {
		t.Fatal("bootstrap accepted a mismatched artifact")
	}
	// The seeding pass really did run — this is the branch the old test skipped by
	// pre-seeding a stack, where creates == 0 was true for free.
	if cfn.creates != 1 {
		t.Fatalf("the seeding pass did not run (%d creates); this test is not exercising the virgin-account path", cfn.creates)
	}
	if cfn.updates != 0 {
		t.Errorf("the stack was updated around a refused artifact (%d updates)", cfn.updates)
	}
	if strings.Contains(cfn.templates[StackName], "ImageOptimizer") {
		t.Error("the seeded stack carries an optimizer whose artifact was refused")
	}

	deployed, err := CheckDeployed(context.Background(), cfn)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	if deployed.Version >= RequiredBootstrapVersion {
		t.Errorf("the seeded stack stamped version %d, which satisfies the gate with no optimizer", deployed.Version)
	}
	if got := CheckCompat(deployed.Version, deployed.Present, RequiredBootstrapVersion); got != NeedsBootstrapUpgrade {
		t.Errorf("CheckCompat = %v, want NeedsBootstrapUpgrade so the operator is told to re-run bootstrap", got)
	}
}

// TestRun_FirstBootstrapStampsTheRequiredVersionOnlyWhenComplete pins both
// halves of the version contract the seeding pass creates. A completed first
// bootstrap must end at the required version — the sub-8 stamp is transient, not
// a state anything is left in — and an unpinned build, which creates no optimizer
// by design, must still reach it in its single pass.
func TestRun_FirstBootstrapStampsTheRequiredVersionOnlyWhenComplete(t *testing.T) {
	for _, tc := range []struct {
		name string
		pin  artifactPin
		body []byte
	}{
		{"pinned and verified", fixturePin(), fixtureArtifact},
		{"unpinned", artifactPin{}, fixtureArtifact},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
			art, _, _ := fixtureArtifactDeps(tc.body)
			ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

			if err := run(context.Background(), cfn, ssmc, iamc, ed, art, tc.pin, productionSubstrate(), nil, nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			deployed, err := CheckDeployed(context.Background(), cfn)
			if err != nil {
				t.Fatalf("CheckDeployed: %v", err)
			}
			if deployed.Version != RequiredBootstrapVersion {
				t.Errorf("stamped version %d, want %d", deployed.Version, RequiredBootstrapVersion)
			}
			if got := CheckCompat(deployed.Version, deployed.Present, RequiredBootstrapVersion); got != Compatible {
				t.Errorf("CheckCompat = %v, want Compatible", got)
			}
		})
	}
}

// TestRun_UnpinnedBuildBootstrapsWithoutAnOptimizer is what every account gets
// until a release asset is cut: a complete bootstrap, no optimizer, no Function
// URL, and therefore an unbound image origin in the worker. It settles in one
// pass — an unpinned build has nothing to upload, so it needs no seeding one.
func TestRun_UnpinnedBuildBootstrapsWithoutAnOptimizer(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	art, store, source := fixtureArtifactDeps(fixtureArtifact)
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := run(context.Background(), cfn, ssmc, iamc, ed, art, artifactPin{}, productionSubstrate(), nil, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if cfn.creates != 1 || cfn.updates != 0 {
		t.Errorf("settled the stack %d creates + %d updates, want a single create", cfn.creates, cfn.updates)
	}
	if len(source.urls) != 0 || store.puts != 0 {
		t.Errorf("an unpinned build touched the network or the bucket: %v, %d puts", source.urls, store.puts)
	}
	if strings.Contains(cfn.templates[StackName], "ImageOptimizer") {
		t.Error("an unpinned build still provisioned an optimizer")
	}
}

// TestCheckDeployed_ReadsTheOptimizerURL proves the deploy path can find the
// Function URL to bind onto the worker, and that a substrate publishing none
// reads back as empty rather than as anything a worker could bind.
func TestCheckDeployed_ReadsTheOptimizerURL(t *testing.T) {
	cfn := newFakeCFN()
	cfn.templates[StackName] = "existing"
	cfn.outputs = map[string]string{outputImageOptimizerURL: "https://abc.lambda-url.us-east-1.on.aws/"}

	deployed, err := CheckDeployed(context.Background(), cfn)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	if deployed.ImageOptimizerURL != "https://abc.lambda-url.us-east-1.on.aws/" {
		t.Errorf("ImageOptimizerURL = %q", deployed.ImageOptimizerURL)
	}

	bare := newFakeCFN()
	bare.templates[StackName] = "existing"
	deployed, err = CheckDeployed(context.Background(), bare)
	if err != nil {
		t.Fatalf("CheckDeployed: %v", err)
	}
	if deployed.ImageOptimizerURL != "" {
		t.Errorf("a substrate with no optimizer read back %q", deployed.ImageOptimizerURL)
	}
}
