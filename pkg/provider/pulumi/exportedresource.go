package pulumi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

type ExportedResource struct {
	Type string
	ID   string
}

const (
	exportedDeploymentVersion = 3
	providerTypePrefix        = "pulumi:providers:"
)

func decodeExportedResources(deployment apitype.UntypedDeployment) ([]ExportedResource, error) {
	if deployment.Version != exportedDeploymentVersion {
		return nil, fmt.Errorf("the stack's state is version %d, and only version %d is readable", deployment.Version, exportedDeploymentVersion)
	}
	var state apitype.DeploymentV3
	if err := json.Unmarshal(deployment.Deployment, &state); err != nil {
		return nil, fmt.Errorf("decode the stack's state: %w", err)
	}
	var exported []ExportedResource
	for _, res := range state.Resources {
		if !res.Custom || res.External || res.Delete || res.ID == "" || strings.HasPrefix(string(res.Type), providerTypePrefix) {
			continue
		}
		exported = append(exported, ExportedResource{
			Type: string(res.Type),
			ID:   string(res.ID),
		})
	}
	return exported, nil
}
