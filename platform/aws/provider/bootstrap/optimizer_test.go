package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const fixtureBucket = "ocel-artifacts-test"

func fixtureOptimizerCode() payloads.Placement {
	return payloads.Placement{Bucket: fixtureBucket, Key: payloads.Key(optimizerKeyPrefix, payloads.ImageOptimizer().SHA256)}
}

type fakeObjectStore struct {
	objects      map[string][]byte
	checksums    map[string]string
	headModes    []s3types.ChecksumMode
	putChecksums []string
	puts         int
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}, checksums: map[string]string{}}
}

func (f *fakeObjectStore) putVerified(key string, body []byte) {
	sum := sha256.Sum256(body)
	f.objects[key] = body
	f.checksums[key] = base64.StdEncoding.EncodeToString(sum[:])
}

func (f *fakeObjectStore) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
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

func (f *fakeObjectStore) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
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

func substratePayloads() map[string]payloads.Payload {
	return map[string]payloads.Payload{
		optimizerKeyPrefix:      payloads.ImageOptimizer(),
		tagPublisherKeyPrefix:   payloads.TagPublisher(),
		tagInvalidatorKeyPrefix: payloads.TagInvalidator(),
		revalidatorKeyPrefix:    payloads.Revalidator(),
	}
}

func preloadedStore() *fakeObjectStore {
	store := newFakeObjectStore()
	for prefix, p := range substratePayloads() {
		store.putVerified(payloads.Key(prefix, p.SHA256), p.Bytes)
	}
	return store
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
						Effect    string `yaml:"Effect"`
						Action    any    `yaml:"Action"`
						Resource  any    `yaml:"Resource"`
						Condition any    `yaml:"Condition"`
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

func TestStackTemplateOptimizer(t *testing.T) {
	t.Run("optimizer compute matches the payload", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			template string
		}{
			{"production", featureTemplate(FeatureImageOptimization, ClassProduction)},
			{"preview", featureTemplate(FeatureImageOptimization, ClassPreview)},
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
					t.Errorf("Architectures = %v, want [arm64] — the payload ships arm64 sharp binaries", p.Architectures)
				}
				if p.Handler != "index.handler" {
					t.Errorf("Handler = %q, want index.handler", p.Handler)
				}
				if p.MemorySize != 1769 {
					t.Errorf("MemorySize = %d, want 1769 (a full vCPU)", p.MemorySize)
				}
				if p.Timeout <= 14 {
					t.Errorf("Timeout = %d, which is under the payload's own summed deadlines", p.Timeout)
				}
				if p.Code.S3Bucket != fixtureOptimizerCode().Bucket || p.Code.S3Key != fixtureOptimizerCode().Key {
					t.Errorf("Code = %s/%s, want the payload placed in this account", p.Code.S3Bucket, p.Code.S3Key)
				}
				if got := p.Environment.Variables[optimizerBucketEnvVar]; got != paramAssetBucketName {
					t.Errorf("%s = %q, want this substrate's own asset bucket", optimizerBucketEnvVar, got)
				}
				if !strings.Contains(tc.template, optimizerBucketEnvVar+": !Ref "+paramAssetBucketName) {
					t.Errorf("%s is not a reference to the asset bucket the core stack handed over", optimizerBucketEnvVar)
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
	})

	t.Run("optimizer reads only its own substrate asset", func(t *testing.T) {
		tmpl := parseOptimizerTemplate(t, featureTemplate(FeatureImageOptimization, ClassProduction))
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
		resources, ok := statements[0].Resource.([]any)
		if !ok || len(resources) != 2 {
			t.Fatalf("Resource = %v, want exactly the two prefixes it reads", statements[0].Resource)
		}
		for _, want := range []string{"${AssetBucketArn}/*/assets/*", "${AssetBucketArn}/*/image-config.json"} {
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
		for _, r := range resources {
			s, ok := r.(string)
			if !ok {
				continue
			}
			if strings.Contains(s, "/isr/") || strings.Contains(s, "/fn/") || strings.HasSuffix(s, ".Arn}/*") {
				t.Errorf("Resource %q reaches past the two leaves the optimizer reads", s)
			}
		}
	})

}

func TestEdgeUserOptimizer(t *testing.T) {
	t.Run("optimizer invoke is its own named statement", func(t *testing.T) {
		template := featureTemplate(FeatureCloudflareEdge, ClassProduction)
		tmpl := parseOptimizerTemplate(t, template)
		user, ok := tmpl.Resources["EdgeUser"]
		if !ok {
			t.Fatal("no edge user in the template")
		}
		statements := user.Properties.Policies[0].PolicyDocument.Statement

		var tagged, named int
		for _, s := range statements {
			actions, ok := s.Action.([]any)
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
				if res != paramImageOptimizerARN {
					t.Errorf("the optimizer's invoke grant names %q, want the ARN the optimizer stack handed over", res)
				}
			}
		}
		if tagged != 1 {
			t.Errorf("found %d tag-conditioned Lambda statements, want exactly 1 left intact", tagged)
		}
		if named != 1 {
			t.Errorf("found %d unconditioned Lambda statements, want exactly 1 naming the optimizer", named)
		}
	})

	t.Run("no optimizer alongside is no invoke grant", func(t *testing.T) {
		template := featureTemplateWith(FeatureCloudflareEdge, ClassProduction, FeatureSet{FeatureISR: true, FeatureCloudflareEdge: true})
		if strings.Contains(template, paramImageOptimizerARN) {
			t.Errorf("the edge reader is granted an optimizer this substrate does not carry:\n%s", template)
		}
	})
}

func TestRunOptimizer(t *testing.T) {
	t.Run("first bootstrap places the payload before the stack that names it", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		store := newFakeObjectStore()
		standInCloudflare(t, &fakeEdge{kind: "cloudflare"})

		if err := runAll(context.Background(), apisOf(cfn, ssmc, iamc, store), productionSubstrate()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if cfn.creates != 4 || cfn.updates != 0 {
			t.Errorf("settled the substrate in %d creates + %d updates, want one create each for core and its three features", cfn.creates, cfn.updates)
		}
		final := cfn.template(optStack(ClassProduction))
		if !strings.Contains(final, "AWS::Lambda::Url") {
			t.Errorf("the settled template carries no optimizer:\n%s", final)
		}
		if !strings.Contains(final, payloads.Key(optimizerKeyPrefix, payloads.ImageOptimizer().SHA256)) {
			t.Error("the settled template does not point at the uploaded payload")
		}
	})

	t.Run("an account already holding the payloads uploads nothing", func(t *testing.T) {
		cfn, ssmc, iamc := newFakeCFN(), newFakeSSM(), &fakeIAM{}
		store := preloadedStore()
		standInCloudflare(t, &fakeEdge{kind: "cloudflare"})

		if err := runAll(context.Background(), apisOf(cfn, ssmc, iamc, store), productionSubstrate()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if store.puts != 0 {
			t.Errorf("uploaded %d payloads into an account that already holds them", store.puts)
		}
	})
}

func TestCheckDeployedOptimizer(t *testing.T) {
	t.Run("reads the optimizer URL", func(t *testing.T) {
		cfn := newFakeCFN()
		cfn.seed(StackName, "Outputs:\n")
		cfn.seed(optStack(ClassProduction), "Outputs:\n  "+outputImageOptimizerURL+":\n    Value: 'https://abc.lambda-url.us-east-1.on.aws/'\n")

		deployed, err := CheckDeployed(context.Background(), cfn)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if want := cfn.output(optStack(ClassProduction), outputImageOptimizerURL); deployed.ImageOptimizerURL != want {
			t.Errorf("ImageOptimizerURL = %q, want %q", deployed.ImageOptimizerURL, want)
		}

		bare := newFakeCFN()
		bare.seed(StackName, "Outputs:\n")
		deployed, err = CheckDeployed(context.Background(), bare)
		if err != nil {
			t.Fatalf("CheckDeployed: %v", err)
		}
		if deployed.ImageOptimizerURL != "" {
			t.Errorf("a substrate with no optimizer read back %q", deployed.ImageOptimizerURL)
		}
	})
}
