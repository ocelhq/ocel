package cloudfront

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type edgeSet struct {
	keyValueStoreARN    string
	functionARN         string
	cachePolicy         string
	headersPolicy       string
	originAccessControl string
}

func edgeSetOf(deployed bootstrap.Deployed, class edge.Class) (edgeSet, error) {
	set := edgeSet{
		keyValueStoreARN:    deployed.Outputs[bootstrap.OutputEdgeRoutesStoreARN],
		functionARN:         deployed.Outputs[bootstrap.OutputEdgeResolverARN],
		cachePolicy:         deployed.Outputs[bootstrap.OutputEdgeCachePolicy],
		headersPolicy:       deployed.Outputs[bootstrap.OutputEdgeHeadersPolicy],
		originAccessControl: deployed.Outputs[bootstrap.OutputEdgeAssetAccess],
	}
	if set.keyValueStoreARN == "" || set.functionARN == "" || set.cachePolicy == "" || set.headersPolicy == "" || set.originAccessControl == "" {
		return edgeSet{}, unbootstrapped(class)
	}
	return set, nil
}

func unbootstrapped(class edge.Class) error {
	return fmt.Errorf("the %s bootstrap in this account carries nothing the %q edge fronts deployments with: its resolver function, key value store and cache policies stand in the %s feature stack, and this account has none. Run `%s` with this edge selected, then deploy again", class, Kind, bootstrap.FeatureCloudFrontEdge, providerkit.BootstrapCommand(class))
}
