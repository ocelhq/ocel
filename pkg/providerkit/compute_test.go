package providerkit_test

import (
	"slices"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestTheWirePinAndTheKitNameTheSameComputes(t *testing.T) {
	field := (&contractv1.ManifestApp{}).ProtoReflect().Descriptor().Fields().ByName("compute")
	if field == nil {
		t.Fatal("ManifestApp has no compute field, so nothing pins the vocabulary on the wire")
	}

	rules, ok := proto.GetExtension(field.Options(), validate.E_Field).(*validate.FieldRules)
	if !ok || rules.GetString() == nil {
		t.Fatal("ManifestApp.compute carries no buf.validate string rule, so the wire admits any compute the kit has never heard of")
	}

	pinned := slices.Sorted(slices.Values(rules.GetString().GetIn()))
	known := slices.Sorted(slices.Values(providerkit.ComputeNames(providerkit.Computes())))
	if !slices.Equal(pinned, known) {
		t.Errorf("ManifestApp.compute pins %v, Computes() names %v — a compute in one list and not the other is either refused by a protovalidate error naming a field the user never wrote, or admitted onto the wire with no provider that runs it", pinned, known)
	}
}
