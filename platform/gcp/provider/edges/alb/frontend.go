package alb

import (
	"strings"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/certificatemanager"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	externalManaged = "EXTERNAL_MANAGED"
	premiumTier     = "PREMIUM"
	httpsPortRange  = "443"
	cacheOnOrigin   = "USE_ORIGIN_HEADERS"
	serverlessNEG   = "SERVERLESS"
	certificateHost = "//certificatemanager.googleapis.com/projects/%s/locations/global/certificateMaps/%s"
)

var frontRouting = []string{"hostRules", "pathMatchers"}

type names struct {
	Address        string
	NotFound       string
	CertificateMap string
	URLMap         string
	Proxy          string
	Rule           string
}

func dashed(parts ...string) string { return strings.Join(parts, "-") }

func frontNames(class edge.Class) names {
	stem := dashed("ocel", string(Kind), string(class))
	return names{
		Address:        stem + "-address",
		NotFound:       stem + "-notfound",
		CertificateMap: stem + "-certs",
		URLMap:         stem + "-routes",
		Proxy:          stem + "-https",
		Rule:           stem + "-forward",
	}
}

type frontSpec struct {
	Project string
	Names   names
}

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

func frontProgram(spec frontSpec) Program {
	return func(ctx *pulumi.Context) error {
		project := pulumi.String(spec.Project)
		address, err := compute.NewGlobalAddress(ctx, spec.Names.Address, &compute.GlobalAddressArgs{
			Name:        pulumi.String(spec.Names.Address),
			Project:     project,
			AddressType: pulumi.String("EXTERNAL"),
		})
		if err != nil {
			return err
		}
		notFound, err := compute.NewBackendService(ctx, spec.Names.NotFound, &compute.BackendServiceArgs{
			Name:                pulumi.String(spec.Names.NotFound),
			Project:             project,
			Protocol:            pulumi.String("HTTPS"),
			LoadBalancingScheme: pulumi.String(externalManaged),
			Description: pulumi.String(
				"answers every hostname no project has claimed, so an unclaimed name reaches nothing rather than whichever project bound first"),
		})
		if err != nil {
			return err
		}
		certificates, err := certificatemanager.NewCertificateMapResource(ctx, spec.Names.CertificateMap,
			&certificatemanager.CertificateMapResourceArgs{
				Name:    pulumi.String(spec.Names.CertificateMap),
				Project: project,
			})
		if err != nil {
			return err
		}
		routes, err := compute.NewURLMap(ctx, spec.Names.URLMap, &compute.URLMapArgs{
			Name:               pulumi.String(spec.Names.URLMap),
			Project:            project,
			DefaultService:     notFound.SelfLink,
			DefaultRouteAction: refusingRouteAction(),
		}, pulumi.IgnoreChanges(frontRouting))
		if err != nil {
			return err
		}
		proxy, err := compute.NewTargetHttpsProxy(ctx, spec.Names.Proxy, &compute.TargetHttpsProxyArgs{
			Name:           pulumi.String(spec.Names.Proxy),
			Project:        project,
			UrlMap:         routes.SelfLink,
			CertificateMap: pulumi.Sprintf(certificateHost, spec.Project, spec.Names.CertificateMap),
		})
		if err != nil {
			return err
		}
		if _, err := compute.NewGlobalForwardingRule(ctx, spec.Names.Rule, &compute.GlobalForwardingRuleArgs{
			Name:                pulumi.String(spec.Names.Rule),
			Project:             project,
			Target:              proxy.SelfLink,
			IpAddress:           address.Address,
			PortRange:           pulumi.String(httpsPortRange),
			LoadBalancingScheme: pulumi.String(externalManaged),
			NetworkTier:         pulumi.String(premiumTier),
		}); err != nil {
			return err
		}
		ctx.Export(outputAddress, address.Address)
		ctx.Export(outputCertificateMap, certificates.Name)
		ctx.Export(outputURLMap, routes.Name)
		ctx.Export(outputNotFound, notFound.Name)
		return nil
	}
}
