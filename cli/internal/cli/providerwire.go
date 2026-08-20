package cli

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func providerConfig(provider *projectconfig.ProviderDescriptor) (*deploymentsv1.ProviderConfig, error) {
	config := &deploymentsv1.ProviderConfig{}
	if provider == nil || len(provider.Options) == 0 {
		return config, nil
	}
	if err := protojson.Unmarshal(provider.Options, config); err != nil {
		return nil, fmt.Errorf("%s configures %s with options it does not accept: %w", projectconfig.ConfigFileName, provider.Package, err)
	}
	return config, nil
}
