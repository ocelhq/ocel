package cloudv1_test

import (
	"strings"
	"testing"

	"buf.build/go/protovalidate"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestProtovalidateAcceptsAnAppNameOfFortyCharacters(t *testing.T) {
	t.Parallel()

	if err := protovalidate.Validate(&cloudv1.CreateAppRequest{Name: strings.Repeat("a", 40)}); err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
}

func TestProtovalidateRefusesAnAppNameOcelCloudCannotHold(t *testing.T) {
	t.Parallel()

	for name, given := range map[string]string{
		"empty":                  "",
		"over forty characters":  strings.Repeat("a", 41),
		"upper case":             "Web",
		"ending in a hyphen":     "web-",
		"starting with a hyphen": "-web",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := protovalidate.Validate(&cloudv1.CreateAppRequest{Name: given}); err == nil {
				t.Fatalf("Validate(%q) = nil, want a refusal", given)
			}
		})
	}
}

func TestProtovalidateAcceptsADeployOfAManifestAndItsImages(t *testing.T) {
	t.Parallel()

	request := &cloudv1.DeployRequest{
		App:      "web",
		Manifest: &contractv1.Manifest{Slug: "shop"},
		Images:   map[string]string{"web": digest},
	}
	if err := protovalidate.Validate(request); err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
}

func TestProtovalidateRefusesADeployWithoutAnImageByDigest(t *testing.T) {
	t.Parallel()

	for name, images := range map[string]map[string]string{
		"no images":             nil,
		"an image by tag":       {"web": "web:latest"},
		"an image by no name":   {"": digest},
		"a short digest":        {"web": "sha256:abc"},
		"a digest of upper hex": {"web": strings.ToUpper(digest)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := protovalidate.Validate(&cloudv1.DeployRequest{App: "web", Images: images}); err == nil {
				t.Fatalf("Validate(%v) = nil, want a refusal", images)
			}
		})
	}
}
