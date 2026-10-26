package container

import (
	"maps"
	"path"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/dispatchpolicy"
)

const (
	imageOptimizerPolicyName = "image-optimizer-invoke"
)

func newFunctionEnv(app *provider.AppSpec, at Target) map[string]string {
	env := map[string]string{}
	if app.Router != "" {
		env[processenv.RouterKindEnvVar] = string(app.Router)
	}
	routing := app.Routing
	if routing == nil {
		return env
	}
	env[edge.OriginDispatchVar] = "1"
	env[edge.OriginSignedVar] = "1"
	if routing.RouteTable.Format == edge.RouteTableNext {
		env[processenv.RouteTableEnvVar] = path.Join(images.FunctionImageRoot, edge.NextRouteTableFile)
	}
	env[processenv.BuildIDEnvVar] = app.BuildID
	maps.Copy(env, processenv.StorageEnv(app.StorageCoordinate, naming.Buckets{naming.StorageEdge: at.Buckets[naming.StorageEdge]}))
	if at.ImageOptimizerURL != "" {
		env[edge.ImageOptimizerURLVar] = at.ImageOptimizerURL
	}
	if rules := app.Static.Variable(); rules != "" {
		env[edge.StaticRulesVar] = rules
	}
	return env
}

func newFunctionPolicies(app *provider.AppSpec, at Target) (map[string]string, error) {
	policies := map[string]string{}
	if app.Routing == nil {
		return policies, nil
	}
	if bucket := at.Buckets[naming.StorageEdge]; bucket != "" {
		document, err := dispatchpolicy.RenderAssetRead(bucket, app.StorageCoordinate)
		if err != nil {
			return nil, err
		}
		policies[dispatchpolicy.AssetReadName] = document
	}
	if at.ImageOptimizerURL != "" {
		document, err := dispatchpolicy.RenderInvoke(nil, component.AccountOfARN(at.AppBoundaryARN), true)
		if err != nil {
			return nil, err
		}
		policies[imageOptimizerPolicyName] = document
	}
	return policies, nil
}
