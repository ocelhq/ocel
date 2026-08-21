package bootstrap

import (
	"strings"
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

		want := Stamp{Schema: 3, Digest: "abc", WrittenBy: "1.2.3", AutoHeal: true}
		if got := readStamp(stampTags(want)); got != want {
			t.Fatalf("readStamp(stampTags(%+v)) = %+v", want, got)
		}
	})

	t.Run("auto-heal off is written, not dropped", func(t *testing.T) {
		t.Parallel()

		tags := stampTags(Stamp{Schema: 1, Digest: "abc", WrittenBy: "1.2.3"})
		var found bool
		for _, tag := range tags {
			if aws.ToString(tag.Key) == TagAutoHeal {
				found = true
				if aws.ToString(tag.Value) != "false" {
					t.Fatalf("%s = %q, want false", TagAutoHeal, aws.ToString(tag.Value))
				}
			}
		}
		if !found {
			t.Fatalf("stampTags did not write %s", TagAutoHeal)
		}
	})

	t.Run("missing schema tag reads as zero", func(t *testing.T) {
		t.Parallel()

		got := readStamp([]cfntypes.Tag{{Key: aws.String("unrelated"), Value: aws.String("9")}})
		if got.Schema != 0 {
			t.Fatalf("Schema = %d, want 0 when the tag is absent", got.Schema)
		}
		if got.Digest != "" || got.WrittenBy != "" || got.AutoHeal {
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

func TestWriterFor(t *testing.T) {
	t.Parallel()

	t.Run("a release version parses", func(t *testing.T) {
		t.Parallel()

		for _, raw := range []string{"1.2.3", "v1.2.3", "0.0.1", "1.2.3-rc.1", "1.2.3+meta"} {
			if w := WriterFor(raw); !w.Release() {
				t.Errorf("WriterFor(%q).Release() = false, want true", raw)
			}
		}
	})

	t.Run("a dev build never parses as a version", func(t *testing.T) {
		t.Parallel()

		for _, raw := range []string{"", "dev", "(devel)", "dev+cafe", "1.2", "1.2.3.4", "nightly"} {
			if w := Writer(raw); w.Release() {
				t.Errorf("Writer(%q).Release() = true, want false", raw)
			}
		}
	})

	t.Run("a dev build stamps its revision", func(t *testing.T) {
		t.Parallel()

		w := writerFor("dev", "cafebabe")
		if string(w) != "dev+cafebabe" {
			t.Fatalf("writerFor(dev, cafebabe) = %q, want dev+cafebabe", w)
		}
		if w.Release() {
			t.Fatalf("%q must never read as a release", w)
		}
	})

	t.Run("a dev build without a revision is still dev", func(t *testing.T) {
		t.Parallel()

		if w := writerFor("dev", ""); string(w) != "dev" {
			t.Fatalf("writerFor(dev, \"\") = %q, want dev", w)
		}
	})

	t.Run("an unset writer reads as unknown", func(t *testing.T) {
		t.Parallel()

		if got := Writer("").String(); got != "unknown" {
			t.Fatalf("Writer(\"\").String() = %q, want unknown", got)
		}
	})

	t.Run("the live writer is never empty", func(t *testing.T) {
		t.Parallel()

		if got := WriterFor("dev"); !strings.HasPrefix(string(got), "dev") {
			t.Fatalf("WriterFor(dev) = %q, want it to start with dev", got)
		}
	})
}
