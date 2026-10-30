package albprogram

import (
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/certificatemanager"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

const (
	cacheOnOrigin   = "USE_ORIGIN_HEADERS"
	certificateHost = "//certificatemanager.googleapis.com/projects/%s/locations/global/certificateMaps/%s"
)

var claimedRouting = []string{"hostRules", "pathMatchers"}

const notFoundStatus = 404

func refusingRouteAction() compute.URLMapDefaultRouteActionPtrInput {
	return &compute.URLMapDefaultRouteActionArgs{
		FaultInjectionPolicy: &compute.URLMapDefaultRouteActionFaultInjectionPolicyArgs{
			Abort: &compute.URLMapDefaultRouteActionFaultInjectionPolicyAbortArgs{
				HttpStatus: pulumi.Int(notFoundStatus),
				Percentage: pulumi.Float64(100),
			},
		},
	}
}

func markingHeaderAction() compute.URLMapHeaderActionPtrInput {
	return &compute.URLMapHeaderActionArgs{
		ResponseHeadersToAdds: compute.URLMapHeaderActionResponseHeadersToAddArray{
			&compute.URLMapHeaderActionResponseHeadersToAddArgs{
				HeaderName:  pulumi.String(router.HeaderRouter),
				HeaderValue: pulumi.String(string(albresources.Kind)),
				Replace:     pulumi.Bool(true),
			},
		},
	}
}

func previewWildcardResources(ctx *pulumi.Context, spec albresources.LoadBalancerSpec, project string) error {
	base := spec.Preview.BaseDomain
	if base == "" {
		return nil
	}
	entry, err := albresources.PreviewEntryName(spec.Namespace, base)
	if err != nil {
		return err
	}
	_, err = certificatemanager.NewCertificateMapEntry(ctx, entry, &certificatemanager.CertificateMapEntryArgs{
		Name:         pulumi.String(entry),
		Project:      pulumi.String(project),
		Map:          pulumi.String(spec.Names.CertificateMap),
		Hostname:     pulumi.String(edge.PreviewWildcard(base)),
		Certificates: pulumi.StringArray{pulumi.String(spec.Preview.Certificate)},
	})
	return err
}

func LoadBalancer(spec albresources.LoadBalancerSpec) (Program, error) {
	patches, err := newFrontPatches(spec.Names, spec.Tier, spec.Patches)
	if err != nil {
		return Program{}, err
	}
	return Program{Patches: patches.applied, Run: func(ctx *pulumi.Context, project string) error {
		projectID := pulumi.String(project)
		addressArgs := &compute.GlobalAddressArgs{
			Name:        pulumi.String(spec.Names.Address),
			Project:     projectID,
			AddressType: pulumi.String("EXTERNAL"),
		}
		if patches.labels != nil {
			addressArgs.Labels = patches.labels
		}
		address, err := compute.NewGlobalAddress(ctx, spec.Names.Address, addressArgs)
		if err != nil {
			return err
		}
		notFound, err := compute.NewBackendService(ctx, spec.Names.NotFound, &compute.BackendServiceArgs{
			Name:                pulumi.String(spec.Names.NotFound),
			Project:             projectID,
			Protocol:            pulumi.String("HTTPS"),
			LoadBalancingScheme: pulumi.String(albresources.ExternalManaged),
			Description: pulumi.String(
				"answers every hostname no project has claimed, so an unclaimed name reaches nothing rather than whichever project bound first"),
		})
		if err != nil {
			return err
		}
		if spec.Assets != nil {
			assets, err := declareAssetsBackend(ctx, spec.Names, responseHeaders(spec.Tier, patches.assetsHeaders), *spec.Assets, projectID)
			if err != nil {
				return err
			}
			ctx.Export(albresources.OutputAssetsBackend, assets.Name)
		}
		certificateArgs := &certificatemanager.CertificateMapResourceArgs{
			Name:    pulumi.String(spec.Names.CertificateMap),
			Project: projectID,
		}
		if patches.labels != nil {
			certificateArgs.Labels = patches.labels
		}
		certificates, err := certificatemanager.NewCertificateMapResource(ctx, spec.Names.CertificateMap, certificateArgs)
		if err != nil {
			return err
		}
		routes, err := compute.NewURLMap(ctx, spec.Names.URLMap, &compute.URLMapArgs{
			Name:               pulumi.String(spec.Names.URLMap),
			Project:            projectID,
			DefaultService:     notFound.SelfLink,
			DefaultRouteAction: refusingRouteAction(),
			HeaderAction:       markingHeaderAction(),
		}, pulumi.IgnoreChanges(claimedRouting))
		if err != nil {
			return err
		}
		proxy, err := compute.NewTargetHttpsProxy(ctx, spec.Names.Proxy, &compute.TargetHttpsProxyArgs{
			Name:           pulumi.String(spec.Names.Proxy),
			Project:        projectID,
			UrlMap:         routes.SelfLink,
			CertificateMap: pulumi.Sprintf(certificateHost, project, spec.Names.CertificateMap),
		})
		if err != nil {
			return err
		}
		ruleArgs := &compute.GlobalForwardingRuleArgs{
			Name:                pulumi.String(spec.Names.Rule),
			Project:             projectID,
			Target:              proxy.SelfLink,
			IpAddress:           address.Address,
			PortRange:           pulumi.String(albresources.HTTPSPortRange),
			LoadBalancingScheme: pulumi.String(albresources.ExternalManaged),
			NetworkTier:         pulumi.String(albresources.PremiumTier),
		}
		if patches.labels != nil {
			ruleArgs.Labels = patches.labels
		}
		if _, err := compute.NewGlobalForwardingRule(ctx, spec.Names.Rule, ruleArgs); err != nil {
			return err
		}
		if err := previewWildcardResources(ctx, spec, project); err != nil {
			return err
		}
		ctx.Export(albresources.OutputAddress, address.Address)
		ctx.Export(albresources.OutputCertificateMap, certificates.Name)
		ctx.Export(albresources.OutputURLMap, routes.Name)
		ctx.Export(albresources.OutputNotFound, notFound.Name)
		return nil
	}}, nil
}
