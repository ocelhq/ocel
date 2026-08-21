package cli

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func providerConfig(provider *projectconfig.ProviderDescriptor) (*contractv1.ProviderConfig, error) {
	config := &contractv1.ProviderConfig{}
	if provider == nil || len(provider.Options) == 0 {
		return config, nil
	}
	options := &structpb.Struct{}
	if err := protojson.Unmarshal(provider.Options, options); err != nil {
		return nil, fmt.Errorf("%s configures %s with options that are not a JSON object: %w", projectconfig.ConfigFileName, provider.Package, err)
	}
	config.Options = options
	return config, nil
}
