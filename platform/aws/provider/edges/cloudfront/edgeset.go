package cloudfront

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type edgeSet struct {
	keyValueStoreARN     string
	functionARN          string
	emptyBodyFunctionARN string
	cachePolicy          string
	headersPolicy        string
	originAccessControl  string
}

func edgeSetOf(deployed bootstrap.Deployed, tier environment.Tier) (edgeSet, error) {
	set := edgeSet{
		keyValueStoreARN:     deployed.Outputs[bootstrap.OutputEdgeRoutesStoreARN],
		functionARN:          deployed.Outputs[bootstrap.OutputEdgeResolverARN],
		emptyBodyFunctionARN: deployed.Outputs[bootstrap.OutputEdgeEmptyBodyARN],
		cachePolicy:          deployed.Outputs[bootstrap.OutputEdgeCachePolicy],
		headersPolicy:        deployed.Outputs[bootstrap.OutputEdgeHeadersPolicy],
		originAccessControl:  deployed.Outputs[bootstrap.OutputEdgeAssetAccess],
	}
	if set.keyValueStoreARN == "" || set.functionARN == "" || set.emptyBodyFunctionARN == "" || set.cachePolicy == "" || set.headersPolicy == "" || set.originAccessControl == "" {
		return edgeSet{}, refuseUnbootstrapped(tier)
	}
	return set, nil
}

func refuseUnbootstrapped(tier environment.Tier) error {
	return fmt.Errorf("the %s bootstrap in this account has nothing the %q edge fronts deployments with: its resolver function, key value store and cache policies are provisioned in the %s feature stack, and this account has none. Run `%s` with this edge selected, then deploy again", tier, Kind, bootstrap.FeatureCloudFrontEdge, provider.BootstrapCommand(tier))
}
