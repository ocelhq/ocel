package alb

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/networkservices"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
)

const (
	gateChain       = "gate"
	gateLocation    = "global"
	gateEvent       = "REQUEST_HEADERS"
	gateEveryCall   = "true"
	forwardingRules = "https://www.googleapis.com/compute/v1/projects/%s/global/forwardingRules/%s"
	pluginService   = "projects/%s/locations/global/wasmPlugins/%s"

	outputGateVersion = "gateVersion"
)

type PluginImages interface {
	Ensure(ctx context.Context, tier environment.Tier, plugin []byte) (string, error)

	Delete(ctx context.Context, tier environment.Tier) error
}

type gateVersion struct {
	ID     string `json:"id"`
	Image  string `json:"image"`
	Config string `json:"config"`
}

func newGateVersion(image, config string) gateVersion {
	digest := sha256.Sum256([]byte(image + "\n" + config))
	return gateVersion{ID: "v" + hex.EncodeToString(digest[:8]), Image: image, Config: config}
}

type gateSpec struct {
	Names  names
	Main   gateVersion
	Before []gateVersion
}

func GateStack(tier environment.Tier) string {
	return dashed(string(Kind), "gate", string(tier))
}

func gateProgram(spec gateSpec) Program {
	return func(ctx *pulumi.Context, project string) error {
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
			LoadBalancingScheme: pulumi.String(externalManaged),
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
		ctx.Export(outputGateVersion, pulumi.String(spec.Main.ID))
		return nil
	}
}

func declareGateVersion(version gateVersion) *networkservices.WasmPluginVersionArgs {
	return &networkservices.WasmPluginVersionArgs{
		VersionName:      pulumi.String(version.ID),
		ImageUri:         pulumi.String(version.Image),
		PluginConfigData: pulumi.ToSecret(pulumi.String(base64.StdEncoding.EncodeToString([]byte(version.Config)))).(pulumi.StringOutput),
	}
}
