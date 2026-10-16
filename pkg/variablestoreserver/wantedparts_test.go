package variablestoreserver_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	providercontract "github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

type keylessCipher struct{}

func (keylessCipher) Seal(context.Context, environment.Tier, seal.AssociatedData, []byte) ([]byte, error) {
	return nil, providercontract.WantedPartError{Part: providercontract.VariablesKeyPart, Err: refusal.Refuse(refusal.CodeNotReady, "no key to seal under")}
}

func (keylessCipher) Open(context.Context, environment.Tier, seal.AssociatedData, []byte) ([]byte, error) {
	return nil, providercontract.WantedPartError{Part: providercontract.VariablesKeyPart, Err: refusal.Refuse(refusal.CodeNotReady, "no key to open under")}
}

func TestAValueRefusedForWantOfAPartToSealUnderRecordsTheProjectsWant(t *testing.T) {
	vendor := fake.NewProvider(fake.Options{})
	variables := serve(t, &variablestoreserver.Service{Source: variablestoreserver.FixedBackend{KeyValues: vendor.KeyValues(), Cipher: keylessCipher{}}})

	_, err := variables.SetValue(context.Background(), &variablestorev1.SetValueRequest{
		Tier:       environmentv1.Tier_TIER_PRODUCTION,
		Coordinate: cell("DATABASE_URL"),
		Value:      "postgres://one",
	})
	if err == nil {
		t.Fatal("SetValue() = nil, want the seal's refusal passed on")
	}

	wants, err := stackrecords.ListBootstrapWants(context.Background(), vendor.KeyValues(), environment.TierProduction)
	if err != nil || !slices.Equal(wants[providercontract.VariablesKeyPart], []string{slug}) {
		t.Errorf("ListBootstrapWants() = %v, %v, want %s's want of the variables key", wants, err, slug)
	}
}
