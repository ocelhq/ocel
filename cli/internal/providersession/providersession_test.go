package providersession

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func TestProviderConfigCarriesTheDescriptorOptionsOpaquely(t *testing.T) {
	config, err := providerConfig(&projectconfig.ProviderDescriptor{
		Package: "@ocel/provider-aws",
		Options: json.RawMessage(`{"region":"us-east-1"}`),
	})
	if err != nil {
		t.Fatalf("providerConfig: %v", err)
	}
	if got := config.GetOptions().GetFields()["region"].GetStringValue(); got != "us-east-1" {
		t.Errorf("region = %q, want us-east-1", got)
	}
}

func TestProviderConfigRefusesOptionsThatAreNotAJSONObject(t *testing.T) {
	_, err := providerConfig(&projectconfig.ProviderDescriptor{
		Package: "@ocel/provider-aws",
		Options: json.RawMessage(`["us-east-1"]`),
	})
	if err == nil {
		t.Fatal("providerConfig err = nil, want a non-object options value refused")
	}
	if !strings.Contains(err.Error(), "not a JSON object") {
		t.Errorf("err = %v, want it to say the options are not a JSON object", err)
	}
}

func TestProviderConfigLeavesAnUnconfiguredProviderWithoutOptions(t *testing.T) {
	config, err := providerConfig(&projectconfig.ProviderDescriptor{
		Package: "@ocel/provider-aws",
		Options: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("providerConfig: %v", err)
	}
	if len(config.GetOptions().GetFields()) != 0 {
		t.Errorf("options = %v, want none for a descriptor carrying no options", config.GetOptions())
	}
}
