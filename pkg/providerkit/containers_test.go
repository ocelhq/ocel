package providerkit_test

import (
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"

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

func TestTheWirePinAndTheKitPinTheSameImageIdentity(t *testing.T) {
	field := (&contractv1.ManifestContainer{}).ProtoReflect().Descriptor().Fields().ByName("image")
	if field == nil {
		t.Fatal("ManifestContainer has no image field, so nothing pins the image identity on the wire")
	}

	rules, ok := proto.GetExtension(field.Options(), validate.E_Field).(*validate.FieldRules)
	if !ok || rules.GetString() == nil {
		t.Fatal("ManifestContainer.image carries no buf.validate string rule, so the wire admits any image identity, tag or none")
	}

	if got, want := rules.GetString().GetPattern(), providerkit.PinnedImagePattern; got != want {
		t.Errorf("ManifestContainer.image pins %q, PinnedImagePattern is %q — a ref one admits and the other refuses is either a plan-time refusal of a ref the wire would have taken, or a protovalidate error naming a field the user never wrote", got, want)
	}
}
