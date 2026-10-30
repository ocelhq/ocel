package albprogram

import (
	"context"
	"encoding/base64"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/networkservices"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

const (
	gateChain       = "gate"
	gateLocation    = "global"
	gateEvent       = "REQUEST_HEADERS"
	gateEveryCall   = "true"
	forwardingRules = "https://www.googleapis.com/compute/v1/projects/%s/global/forwardingRules/%s"
	pluginService   = "projects/%s/locations/global/wasmPlugins/%s"
)

func gateProgram(spec albresources.GateSpec) Program {
	return Program{Run: func(ctx *pulumi.Context, project string) error {
		versions := networkservices.WasmPluginVersionArray{declareGateVersion(spec.Main)}
		for _, before := range spec.Before {
			versions = append(versions, declareGateVersion(before))
		}
		plugin, err := networkservices.NewWasmPlugin(ctx, spec.Names.GatePlugin, &networkservices.WasmPluginArgs{
			Name:          pulumi.String(spec.Names.GatePlugin),
			Project:       pulumi.String(project),
			Location:      pulumi.String(gateLocation),
			Description:   pulumi.String("runs the gate in front of Cloud CDN, so a protected project is cached and no cached response reaches a visitor the gate refuses"),
			MainVersionId: pulumi.String(spec.Main.ID),
			Versions:      versions,
		})
		if err != nil {
			return err
		}
		_, err = networkservices.NewLbEdgeExtension(ctx, spec.Names.GateExtension, &networkservices.LbEdgeExtensionArgs{
			Name:                pulumi.String(spec.Names.GateExtension),
			Project:             pulumi.String(project),
			Location:            pulumi.String(gateLocation),
			LoadBalancingScheme: pulumi.String(albresources.ExternalManaged),
			ForwardingRules:     pulumi.StringArray{pulumi.Sprintf(forwardingRules, project, spec.Names.Rule)},
			ExtensionChains: networkservices.LbEdgeExtensionExtensionChainArray{
				&networkservices.LbEdgeExtensionExtensionChainArgs{
					Name:           pulumi.String(gateChain),
					MatchCondition: &networkservices.LbEdgeExtensionExtensionChainMatchConditionArgs{CelExpression: pulumi.String(gateEveryCall)},
					Extensions: networkservices.LbEdgeExtensionExtensionChainExtensionArray{
						&networkservices.LbEdgeExtensionExtensionChainExtensionArgs{
							Name:            pulumi.String(gateChain),
							Service:         pulumi.Sprintf(pluginService, project, spec.Names.GatePlugin),
							FailOpen:        pulumi.Bool(false),
							SupportedEvents: pulumi.StringArray{pulumi.String(gateEvent)},
						},
					},
				},
			},
		}, pulumi.DependsOn([]pulumi.Resource{plugin}))
		if err != nil {
			return err
		}
		ctx.Export(albresources.OutputGateVersion, pulumi.String(spec.Main.ID))
		return nil
	}}
}

func declareGateVersion(version albresources.GateVersion) *networkservices.WasmPluginVersionArgs {
	return &networkservices.WasmPluginVersionArgs{
		VersionName:      pulumi.String(version.ID),
		ImageUri:         pulumi.String(version.Image),
		PluginConfigData: pulumi.ToSecret(pulumi.String(base64.StdEncoding.EncodeToString([]byte(version.Config)))).(pulumi.StringOutput),
	}
}

func Gate(publish func(ctx context.Context, write bool) (albresources.GateSpec, error)) Program {
	return Program{Run: func(ctx *pulumi.Context, project string) error {
		spec, err := publish(ctx.Context(), !ctx.DryRun())
		if err != nil {
			return err
		}
		return gateProgram(spec).Run(ctx, project)
	}}
}
