package bootstrap

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

func TestTemplateDigest(t *testing.T) {
	t.Parallel()

	t.Run("same bytes same digest", func(t *testing.T) {
		t.Parallel()

		body := stackTemplate()
		if TemplateDigest(body) != TemplateDigest(stackTemplate()) {
			t.Fatal("rendering the same template twice must produce the same digest")
		}
	})

	t.Run("different bytes different digest", func(t *testing.T) {
		t.Parallel()

		if TemplateDigest(stackTemplate()) == TemplateDigest(previewStackTemplate()) {
			t.Fatal("two different template bodies must not share a digest")
		}
	})

	t.Run("digest is hex sha256", func(t *testing.T) {
		t.Parallel()

		got := TemplateDigest("")
		if got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
			t.Fatalf("TemplateDigest(\"\") = %q, want the sha256 of no bytes", got)
		}
	})

	t.Run("parameter values are not in the body", func(t *testing.T) {
		t.Parallel()

		in := featureInputs{class: ClassProduction, refs: stackRefs{assetBucket: "bucket-one", assetBucketARN: "arn:one"}}
		other := in
		other.refs = stackRefs{assetBucket: "bucket-two", assetBucketARN: "arn:two"}
		if TemplateDigest(imageOptimizationTemplate(in).body) != TemplateDigest(imageOptimizationTemplate(other).body) {
			t.Fatal("the digest must not move when only a cross-stack parameter value moves")
		}
	})
}

func TestStampTags(t *testing.T) {
	t.Parallel()

	t.Run("round trip", func(t *testing.T) {
		t.Parallel()

		want := Stamp{Schema: 3, Digest: "abc", WrittenBy: "1.2.3"}
		if got := readStamp(stampTags(want)); got != want {
			t.Fatalf("readStamp(stampTags(%+v)) = %+v", want, got)
		}
	})

	t.Run("missing schema tag reads as zero", func(t *testing.T) {
		t.Parallel()

		got := readStamp([]cfntypes.Tag{{Key: aws.String("unrelated"), Value: aws.String("9")}})
		if got.Schema != 0 {
			t.Fatalf("Schema = %d, want 0 when the tag is absent", got.Schema)
		}
		if got.Digest != "" || got.WrittenBy != "" {
			t.Fatalf("readStamp of unrelated tags = %+v, want the zero stamp", got)
		}
	})

	t.Run("unreadable schema tag reads as zero", func(t *testing.T) {
		t.Parallel()

		got := readStamp([]cfntypes.Tag{{Key: aws.String(TagSchema), Value: aws.String("twelve")}})
		if got.Schema != 0 {
			t.Fatalf("Schema = %d, want 0 when the tag cannot be read as a number", got.Schema)
		}
	})
}
