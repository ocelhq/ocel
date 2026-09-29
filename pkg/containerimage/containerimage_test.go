package containerimage_test

import (
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ocelhq/ocel/pkg/containerimage"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestAPinnedImageNamesARegistryHostButNeverATag(t *testing.T) {
	const digest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	for _, ref := range []string{
		"ocel/api" + digest,
		"registry.example.com:5000/ocel/api" + digest,
		"localhost:5000/api" + digest,
		"api" + digest,
	} {
		if !containerimage.IsPinned(ref) {
			t.Errorf("IsPinned(%q) = false, want an ordinary OCI reference admitted: a registry answering on a port is where a pushed image lives", ref)
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
		if containerimage.IsPinned(ref) {
			t.Errorf("IsPinned(%q) = true, want it refused: a tag repoints under a running release, so it never rides in the identity a release is pinned to", ref)
		}
	}
}

func TestAProbedPathIsOnePathOffTheRootAndNothingElse(t *testing.T) {
	for _, path := range []string{"/", "/healthz", "/up/ready", "/up-ready.json"} {
		if !containerimage.IsHealthCheckPath(path) {
			t.Errorf("IsHealthCheckPath(%q) = false, want a path off the app's root admitted", path)
		}
	}

	for _, path := range []string{"", "healthz", "/up?ready=1", "/up#ready", "/up ready", "/up\tready", "/up\nready"} {
		if containerimage.IsHealthCheckPath(path) {
			t.Errorf("IsHealthCheckPath(%q) = true, want it refused: a probe asks one path of the process and has no query, fragment or whitespace to ask it with", path)
		}
	}
}

func TestTheContractAndContainerimagePinTheSameImageIdentity(t *testing.T) {
	assertFieldPattern(t, "image", containerimage.PinnedPattern)
}

func TestTheContractAndContainerimagePinTheSameProbedPath(t *testing.T) {
	assertFieldPattern(t, "health_check_path", containerimage.HealthCheckPathPattern)
}

func assertFieldPattern(t *testing.T, name, want string) {
	t.Helper()

	field := (&contractv1.ContainerArtifact{}).ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(name))
	if field == nil {
		t.Fatalf("ContainerArtifact has no %s field, so nothing pins it in the contract", name)
	}

	rules, ok := proto.GetExtension(field.Options(), validate.E_Field).(*validate.FieldRules)
	if !ok || rules.GetString() == nil {
		t.Fatalf("ContainerArtifact.%s has no buf.validate string rule, so the contract admits anything at all there", name)
	}

	if got := rules.GetString().GetPattern(); got != want {
		t.Errorf("ContainerArtifact.%s pins %q, containerimage pins %q — a value one admits and the other refuses is either a plan-time refusal of something the contract would have taken, or a protovalidate error naming a field the user never wrote", name, got, want)
	}
}
