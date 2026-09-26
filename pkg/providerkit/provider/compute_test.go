package provider_test

import (
	"slices"
	"strings"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"google.golang.org/protobuf/proto"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
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
	known := slices.Sorted(slices.Values(provider.ComputeNames(provider.Computes())))
	if !slices.Equal(pinned, known) {
		t.Errorf("ManifestApp.compute pins %v, Computes() names %v — a compute in one list and not the other is either refused by a protovalidate error naming a field the user never wrote, or admitted onto the wire with no provider that runs it", pinned, known)
	}
}

func TestConnectorComputeDefaultsToTheFirstSupported(t *testing.T) {
	held, err := provider.ConnectorCompute("", provider.ComputeContainer, provider.ComputeServerless)
	if err != nil {
		t.Fatalf("ConnectorCompute(\"\", container, serverless) = %v, want the target to pick for itself", err)
	}
	if held != provider.ComputeContainer {
		t.Errorf("ConnectorCompute(\"\", container, serverless) = %q, want %q", held, provider.ComputeContainer)
	}
}

func TestConnectorComputeTakesAnySupported(t *testing.T) {
	held, err := provider.ConnectorCompute(provider.ComputeServerless, provider.ComputeContainer, provider.ComputeServerless)
	if err != nil {
		t.Fatalf("ConnectorCompute(serverless, container, serverless) = %v, want it taken", err)
	}
	if held != provider.ComputeServerless {
		t.Errorf("ConnectorCompute(serverless, ...) = %q, want %q", held, provider.ComputeServerless)
	}
}

func TestConnectorComputeRefusesWhatTheTargetDoesNotHandOut(t *testing.T) {
	_, err := provider.ConnectorCompute(provider.ComputeContainer, provider.ComputeServerless)
	if err == nil {
		t.Fatal("ConnectorCompute(container, serverless) = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), string(provider.ComputeServerless)) {
		t.Errorf("ConnectorCompute(container, serverless) = %q, want it to name what the target does run", err)
	}
}

func TestConnectorComputeRefusesATargetThatSupportsNothing(t *testing.T) {
	if _, err := provider.ConnectorCompute(""); err == nil {
		t.Fatal("ConnectorCompute(\"\") = nil, want a refusal")
	}
}
