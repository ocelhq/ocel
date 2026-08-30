package providerkit_test

import (
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestAPinnedImageCarriesARegistryHostButNeverATag(t *testing.T) {
	const digest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	for _, ref := range []string{
		"ocel/api" + digest,
		"registry.example.com:5000/ocel/api" + digest,
		"localhost:5000/api" + digest,
		"api" + digest,
	} {
		if !providerkit.PinnedImage(ref) {
			t.Errorf("PinnedImage(%q) = false, want an ordinary OCI reference admitted: a registry answering on a port is where a pushed image lives", ref)
		}
	}

	for _, ref := range []string{
		"ocel/api:latest",
		"ocel/api:latest" + digest,
		"registry.example.com:5000/ocel/api:latest" + digest,
		"ocel/api",
		"ocel/api@sha256:short",
		"/ocel/api" + digest,
		"ocel//api" + digest,
	} {
		if providerkit.PinnedImage(ref) {
			t.Errorf("PinnedImage(%q) = true, want it refused: a tag repoints under a running release, so it never rides in the identity a release is pinned to", ref)
		}
	}
}

func TestAProbedPathIsOnePathOffTheRootAndNothingElse(t *testing.T) {
	for _, path := range []string{"/", "/healthz", "/up/ready", "/up-ready.json"} {
		if !providerkit.HealthCheckPath(path) {
			t.Errorf("HealthCheckPath(%q) = false, want a path off the app's root admitted", path)
		}
	}

	for _, path := range []string{"", "healthz", "/up?ready=1", "/up#ready", "/up ready", "/up\tready", "/up\nready"} {
		if providerkit.HealthCheckPath(path) {
			t.Errorf("HealthCheckPath(%q) = true, want it refused: a probe asks one path of the process and carries no query, fragment or whitespace to ask it with", path)
		}
	}
}

func TestTheWirePinAndTheKitPinTheSameImageIdentity(t *testing.T) {
	assertFieldPattern(t, "image", providerkit.PinnedImagePattern)
}

func TestTheWirePinAndTheKitPinTheSameProbedPath(t *testing.T) {
	assertFieldPattern(t, "health_check_path", providerkit.HealthCheckPathPattern)
}

func assertFieldPattern(t *testing.T, name, want string) {
	t.Helper()

	field := (&contractv1.ManifestContainer{}).ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
	if field == nil {
		t.Fatalf("ManifestContainer has no %s field, so nothing pins it on the wire", name)
	}

	rules, ok := proto.GetExtension(field.Options(), validate.E_Field).(*validate.FieldRules)
	if !ok || rules.GetString() == nil {
		t.Fatalf("ManifestContainer.%s carries no buf.validate string rule, so the wire admits anything at all there", name)
	}

	if got := rules.GetString().GetPattern(); got != want {
		t.Errorf("ManifestContainer.%s pins %q, the kit pins %q — a value one admits and the other refuses is either a plan-time refusal of something the wire would have taken, or a protovalidate error naming a field the user never wrote", name, got, want)
	}
}
