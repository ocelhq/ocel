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

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var fixtureArtifact = []byte("PK\x03\x04 pretend this is a Lambda zip")

func fixtureDigest() string {
	sum := sha256.Sum256(fixtureArtifact)
	return hex.EncodeToString(sum[:])
}

func fixturePin() artifactPin {
	return artifactPin{version: "1.2.3", sha256: fixtureDigest()}
}

func fixtureOptimizerCode() artifactCode {
	return artifactCode{bucket: "ocel-artifacts-test", key: optimizerArtifactKey(fixturePin())}
}

type fakeArtifactStore struct {
	objects      map[string][]byte
	checksums    map[string]string
	headModes    []s3types.ChecksumMode
	putChecksums []string
	puts         int
}

func newFakeArtifactStore() *fakeArtifactStore {
	return &fakeArtifactStore{objects: map[string][]byte{}, checksums: map[string]string{}}
}

func (f *fakeArtifactStore) put(key string, body []byte) {
	f.objects[key] = body
}

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

func preloadedArtifact() Artifacts {
	store := newFakeArtifactStore()
	for _, a := range []struct {
		pin   artifactPin
		key   func(artifactPin) string
		label string
	}{
		{pinnedOptimizer(), optimizerArtifactKey, optimizerLabel},
		{pinnedTagPublisher(), tagPublisherArtifactKey, tagPublisherLabel},
		{pinnedRevalidator(), revalidatorArtifactKey, revalidatorLabel},
	} {
		if !a.pin.pinned() {
			continue
		}
		key := a.key(a.pin)
		store.put(key, fixtureArtifact)
		if sum, err := a.pin.checksum(a.label); err == nil {
			store.checksums[key] = sum
		}
	}
	return Artifacts{Source: &fakeArtifactSource{}, Store: store}
}

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
	if !strings.Contains(err.Error(), fixturePin().sha256) {
		t.Errorf("error does not name the required digest: %v", err)
	}
}

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
	for _, got := range store.putChecksums {
		if got == fixtureDigest() {
			t.Error("sent the hex digest as ChecksumSHA256; S3 wants base64 of the raw bytes")
		}
	}
}

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
	if len(store.headModes) != 1 || store.headModes[0] != s3types.ChecksumModeEnabled {
		t.Errorf("head checksum modes %v, want [%s]", store.headModes, s3types.ChecksumModeEnabled)
	}
}

func TestEnsureOptimizerArtifact_DistrustsBytesAtThePinnedKey(t *testing.T) {
	key := optimizerArtifactKey(fixturePin())
	planted := []byte("MZ\x90\x00 an executable nobody reviewed")

	for _, tc := range []struct {
		name string
		seed func(*fakeArtifactStore)
	}{
		{"no stored checksum", func(s *fakeArtifactStore) { s.put(key, planted) }},
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

func TestReleaseSource_DoesNotUseTheDefaultClient(t *testing.T) {
	client := artifactReleaseClient()
	if client.Timeout == 0 {
		t.Error("the release client has no timeout; a stalled host would hang bootstrap")
	}

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

func TestStackTemplate_OptimizerComputeMatchesTheArtifact(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)},
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
			if p.Timeout <= 14 {
				t.Errorf("Timeout = %d, which is under the artifact's own summed deadlines", p.Timeout)
			}
			if p.Code.S3Bucket != fixtureOptimizerCode().bucket || p.Code.S3Key != fixtureOptimizerCode().key {
				t.Errorf("Code = %s/%s, want the artifact uploaded into this account", p.Code.S3Bucket, p.Code.S3Key)
			}
			if got := p.Environment.Variables[optimizerBucketEnvVar]; got != "AssetBucket" {
				t.Errorf("%s = %q, want this substrate's own asset bucket", optimizerBucketEnvVar, got)
			}
			if !strings.Contains(tc.template, optimizerBucketEnvVar+": !Ref AssetBucket") {
				t.Errorf("%s is not a reference to this stack's own AssetBucket", optimizerBucketEnvVar)
			}
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

func TestStackTemplate_OptimizerReadsOnlyItsOwnSubstrateAsset(t *testing.T) {
	tmpl := parseOptimizerTemplate(t, stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion))
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

func TestEdgeUser_OptimizerInvokeIsItsOwnNamedStatement(t *testing.T) {
	template := stackTemplate(edge.TrustExternal, fixtureArtifacts(), RequiredBootstrapVersion)
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

func TestStackTemplate_NoArtifactRendersNoOptimizer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
	}{
		{"production", stackTemplate(edge.TrustExternal, stackArtifacts{}, RequiredBootstrapVersion)},
		{"preview", previewStackTemplate(edge.TrustExternal, stackArtifacts{}, RequiredBootstrapVersion)},
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
			if !strings.Contains(tc.template, "aws:ResourceTag/ocel:app") {
				t.Error("the edge user lost its tag-conditioned Lambda grant")
			}
		})
	}
}

func TestRun_FirstBootstrapSeedsTheBucketThenPlacesTheArtifact(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	art, store, source := fixtureArtifactDeps(fixtureArtifact)
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := run(context.Background(), cfn, ssmc, iamc, ed, art, stackPins{optimizer: fixturePin()}, productionSubstrate(), nil, nil); err != nil {
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

func TestRun_RefusedArtifactLeavesAnExistingAccountAlone(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	cfn.templates[StackName] = "existing"
	art, _, _ := fixtureArtifactDeps([]byte("tampered"))
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	err := run(context.Background(), cfn, ssmc, iamc, ed, art, stackPins{optimizer: fixturePin()}, productionSubstrate(), nil, nil)
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

func TestRun_RefusedArtifactOnAVirginAccountFailsTheGate(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	art, _, _ := fixtureArtifactDeps([]byte("tampered"))
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	err := run(context.Background(), cfn, ssmc, iamc, ed, art, stackPins{optimizer: fixturePin()}, productionSubstrate(), nil, nil)
	if err == nil {
		t.Fatal("bootstrap accepted a mismatched artifact")
	}
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

			if err := run(context.Background(), cfn, ssmc, iamc, ed, art, stackPins{optimizer: tc.pin}, productionSubstrate(), nil, nil); err != nil {
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

func TestRun_UnpinnedBuildBootstrapsWithoutAnOptimizer(t *testing.T) {
	cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
	art, store, source := fixtureArtifactDeps(fixtureArtifact)
	ed := &fakeEdge{out: edge.BootstrapOutput{Trust: edge.TrustExternal}}

	if err := run(context.Background(), cfn, ssmc, iamc, ed, art, stackPins{}, productionSubstrate(), nil, nil); err != nil {
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
