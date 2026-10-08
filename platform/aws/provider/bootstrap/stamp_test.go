package bootstrap

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"

	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

func TestTemplateDigest(t *testing.T) {
	t.Parallel()

	t.Run("same bytes same digest", func(t *testing.T) {
		t.Parallel()

		body := coreStackTemplate(defaultNamespace, environment.TierProduction, "")
		if cfn.TemplateDigest(body) != cfn.TemplateDigest(coreStackTemplate(defaultNamespace, environment.TierProduction, "")) {
			t.Fatal("rendering the same template twice must produce the same digest")
		}
	})

	t.Run("different bytes different digest", func(t *testing.T) {
		t.Parallel()

		if cfn.TemplateDigest(coreStackTemplate(defaultNamespace, environment.TierProduction, "")) == cfn.TemplateDigest(coreStackTemplate(defaultNamespace, environment.TierPreview, "")) {
			t.Fatal("two different template bodies must not share a digest")
		}
	})

	t.Run("digest is hex sha256", func(t *testing.T) {
		t.Parallel()

		got := cfn.TemplateDigest("")
		if got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
			t.Fatalf("cfn.TemplateDigest(\"\") = %q, want the sha256 of no bytes", got)
		}
	})

	t.Run("parameter values are not in the body", func(t *testing.T) {
		t.Parallel()

		in := featureInputs{ns: defaultNamespace, tier: environment.TierProduction, refs: stackRefs{assetBucket: "bucket-one", assetBucketARN: "arn:one"}}
		other := in
		other.refs = stackRefs{assetBucket: "bucket-two", assetBucketARN: "arn:two"}
		if cfn.TemplateDigest(imageOptimizationTemplate(in).body) != cfn.TemplateDigest(imageOptimizationTemplate(other).body) {
			t.Fatal("the digest must not move when only a cross-stack parameter value moves")
		}
	})
}

func TestEveryBootstrapStackIsTaggedWithItsTier(t *testing.T) {
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		t.Run(string(tier), func(t *testing.T) {
			stacks := newFakeCFN()
			if err := Run(context.Background(), apisOf(stacks, newFakeSSM(), &fakeIAM{}, preloadedStore()), defaultNamespace, tier, everything(), nil); err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(stacks.tags) == 0 {
				t.Fatal("the run applied no stack")
			}
			for name, tags := range stacks.tags {
				got := ""
				for _, tag := range tags {
					if aws.ToString(tag.Key) == naming.EnvTierTagKey {
						got = aws.ToString(tag.Value)
					}
				}
				if got != string(tier) {
					t.Errorf("stack %s is tagged %s=%q, want %q: CloudFormation hands the tag to every role and function it makes, and the bootstrap credential reaches only those of its own tier", name, naming.EnvTierTagKey, got, tier)
				}
			}
		})
	}
}

func TestStampTags(t *testing.T) {
	t.Parallel()

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()

		want := Stamp{Digest: "abc", WrittenBy: "1.2.3"}
		if got := readStamp(stampTags(defaultNamespace, environment.TierProduction, want)); got != want {
			t.Fatalf("readStamp(stampTags(defaultNamespace, production, %+v)) = %+v", want, got)
		}
	})

	t.Run("unrelated tags read as the zero stamp", func(t *testing.T) {
		t.Parallel()

		got := readStamp([]cfntypes.Tag{{Key: aws.String("unrelated"), Value: aws.String("9")}})
		if got.Digest != "" || got.WrittenBy != "" {
			t.Fatalf("readStamp of unrelated tags = %+v, want the zero stamp", got)
		}
	})
}
