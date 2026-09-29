package alb

import (
	"strings"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/certificatemanager"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/networksecurity"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/router"
)

const (
	externalManaged    = "EXTERNAL_MANAGED"
	premiumTier        = "PREMIUM"
	httpsPortRange     = "443"
	httpPortRange      = "80"
	permanentRedirect  = "PERMANENT_REDIRECT"
	cacheOnOrigin      = "USE_ORIGIN_HEADERS"
	serverlessNEG      = "SERVERLESS"
	servicePlaceholder = "<service>"
	certificateHost    = "//certificatemanager.googleapis.com/projects/%s/locations/global/certificateMaps/%s"
)

var claimedRouting = []string{"hostRules", "pathMatchers"}

type names struct {
	Address        string
	NotFound       string
	CertificateMap string
	URLMap         string
	Proxy          string
	Rule           string
	Trust          string
	Policy         string
	RedirectURLMap string
	HTTPProxy      string
	HTTPRule       string
}

func dashed(parts ...string) string { return strings.Join(parts, "-") }

func loadBalancerNames(tier environment.Tier, shielded bool) names {
	stem := dashed("ocel", string(Kind), string(tier))
	if shielded {
		stem = dashed("ocel", string(Kind), shieldedNameSegment, string(tier))
	}
	return names{
		Address:        stem + "-address",
		NotFound:       stem + "-notfound",
		CertificateMap: stem + "-certs",
		URLMap:         stem + "-routes",
		Proxy:          stem + "-https",
		Rule:           stem + "-forward",
		Trust:          stem + "-trust",
		Policy:         stem + "-mtls",
		RedirectURLMap: stem + "-redirect",
		HTTPProxy:      stem + "-http",
		HTTPRule:       stem + "-forward-http",
	}
}

type loadBalancerSpec struct {
	Region             string
	Names              names
	Preview            previewEntry
	ClientCertificates []string
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

func markingHeaderAction() compute.URLMapHeaderActionPtrInput {
	return &compute.URLMapHeaderActionArgs{
		ResponseHeadersToAdds: compute.URLMapHeaderActionResponseHeadersToAddArray{
			&compute.URLMapHeaderActionResponseHeadersToAddArgs{
				HeaderName:  pulumi.String(router.HeaderRouter),
				HeaderValue: pulumi.String(string(Kind)),
				Replace:     pulumi.Bool(true),
			},
		},
	}
}

func previewWildcardResources(ctx *pulumi.Context, spec loadBalancerSpec, project string) error {
	base := spec.Preview.BaseDomain
	if base == "" {
		return nil
	}
	projectID := pulumi.String(project)
	entry := previewEntryName(base)
	if _, err := certificatemanager.NewCertificateMapEntry(ctx, entry, &certificatemanager.CertificateMapEntryArgs{
		Name:         pulumi.String(entry),
		Project:      projectID,
		Map:          pulumi.String(spec.Names.CertificateMap),
		Hostname:     pulumi.String(edge.PreviewWildcard(base)),
		Certificates: pulumi.StringArray{pulumi.String(spec.Preview.Certificate)},
	}); err != nil {
		return err
	}
	neg := previewNEGName(base)
	group, err := compute.NewRegionNetworkEndpointGroup(ctx, neg, &compute.RegionNetworkEndpointGroupArgs{
		Name:                pulumi.String(neg),
		Project:             projectID,
		Region:              pulumi.String(spec.Region),
		NetworkEndpointType: pulumi.String(serverlessNEG),
		CloudRun: &compute.RegionNetworkEndpointGroupCloudRunArgs{
			UrlMask: pulumi.String(servicePlaceholder + "." + base),
		},
	})
	if err != nil {
		return err
	}
	backend := previewBackendName(base)
	_, err = compute.NewBackendService(ctx, backend, &compute.BackendServiceArgs{
		Name:                pulumi.String(backend),
		Project:             projectID,
		Protocol:            pulumi.String("HTTPS"),
		LoadBalancingScheme: pulumi.String(externalManaged),
		EnableCdn:           pulumi.Bool(true),
		CdnPolicy: &compute.BackendServiceCdnPolicyArgs{
			CacheMode: pulumi.String(cacheOnOrigin),
			CacheKeyPolicy: &compute.BackendServiceCdnPolicyCacheKeyPolicyArgs{
				IncludeHost:        pulumi.Bool(true),
				IncludeProtocol:    pulumi.Bool(true),
				IncludeQueryString: pulumi.Bool(true),
			},
		},
		Backends: compute.BackendServiceBackendArray{
			&compute.BackendServiceBackendArgs{Group: group.SelfLink},
		},
		Description: pulumi.String(
			"resolves the first label of every preview hostname on " + edge.PreviewWildcard(base) +
				" to the Cloud Run service of that name, so a preview is a deploy and nothing else"),
	})
	return err
}

const (
	rejectInvalidClients = "REJECT_INVALID"
	trustConfigPath      = "projects/%s/locations/global/trustConfigs/%s"
	serverPolicyPath     = "//networksecurity.googleapis.com/projects/%s/locations/global/serverTlsPolicies/%s"
	globalLocation       = "global"
)

func clientValidation(ctx *pulumi.Context, spec loadBalancerSpec, project string) (pulumi.Resource, error) {
	allowed := certificatemanager.TrustConfigAllowlistedCertificateArray{}
	for _, pem := range spec.ClientCertificates {
		allowed = append(allowed, &certificatemanager.TrustConfigAllowlistedCertificateArgs{PemCertificate: pulumi.String(pem)})
	}
	trust, err := certificatemanager.NewTrustConfig(ctx, spec.Names.Trust, &certificatemanager.TrustConfigArgs{
		Name:                    pulumi.String(spec.Names.Trust),
		Project:                 pulumi.String(project),
		Location:                pulumi.String(globalLocation),
		AllowlistedCertificates: allowed,
		Description:             pulumi.String("the client certificates the edge in front of this load balancer presents, and nothing else"),
	})
	if err != nil {
		return nil, err
	}
	policy, err := networksecurity.NewServerTlsPolicy(ctx, spec.Names.Policy, &networksecurity.ServerTlsPolicyArgs{
		Name:     pulumi.String(spec.Names.Policy),
		Project:  pulumi.String(project),
		Location: pulumi.String(globalLocation),
		MtlsPolicy: &networksecurity.ServerTlsPolicyMtlsPolicyArgs{
			ClientValidationMode:        pulumi.String(rejectInvalidClients),
			ClientValidationTrustConfig: pulumi.Sprintf(trustConfigPath, project, spec.Names.Trust),
		},
	}, pulumi.DependsOn([]pulumi.Resource{trust}))
	if err != nil {
		return nil, err
	}
	return policy, nil
}

func redirectToHTTPS(ctx *pulumi.Context, spec loadBalancerSpec, project string, address pulumi.StringOutput) error {
	projectID := pulumi.String(project)
	redirect, err := compute.NewURLMap(ctx, spec.Names.RedirectURLMap, &compute.URLMapArgs{
		Name:    pulumi.String(spec.Names.RedirectURLMap),
		Project: projectID,
		DefaultUrlRedirect: &compute.URLMapDefaultUrlRedirectArgs{
			HttpsRedirect:        pulumi.Bool(true),
			RedirectResponseCode: pulumi.String(permanentRedirect),
			StripQuery:           pulumi.Bool(false),
		},
	})
	if err != nil {
		return err
	}
	proxy, err := compute.NewTargetHttpProxy(ctx, spec.Names.HTTPProxy, &compute.TargetHttpProxyArgs{
		Name:    pulumi.String(spec.Names.HTTPProxy),
		Project: projectID,
		UrlMap:  redirect.SelfLink,
	})
	if err != nil {
		return err
	}
	_, err = compute.NewGlobalForwardingRule(ctx, spec.Names.HTTPRule, &compute.GlobalForwardingRuleArgs{
		Name:                pulumi.String(spec.Names.HTTPRule),
		Project:             projectID,
		Target:              proxy.SelfLink,
		IpAddress:           address,
		PortRange:           pulumi.String(httpPortRange),
		LoadBalancingScheme: pulumi.String(externalManaged),
		NetworkTier:         pulumi.String(premiumTier),
	})
	return err
}

func loadBalancerProgram(spec loadBalancerSpec) Program {
	return func(ctx *pulumi.Context, project string) error {
		projectID := pulumi.String(project)
		address, err := compute.NewGlobalAddress(ctx, spec.Names.Address, &compute.GlobalAddressArgs{
			Name:        pulumi.String(spec.Names.Address),
			Project:     projectID,
			AddressType: pulumi.String("EXTERNAL"),
		})
		if err != nil {
			return err
		}
		notFound, err := compute.NewBackendService(ctx, spec.Names.NotFound, &compute.BackendServiceArgs{
			Name:                pulumi.String(spec.Names.NotFound),
			Project:             projectID,
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
				Project: projectID,
			})
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
		proxyArgs := &compute.TargetHttpsProxyArgs{
			Name:           pulumi.String(spec.Names.Proxy),
			Project:        projectID,
			UrlMap:         routes.SelfLink,
			CertificateMap: pulumi.Sprintf(certificateHost, project, spec.Names.CertificateMap),
		}
		var validating []pulumi.ResourceOption
		if len(spec.ClientCertificates) > 0 {
			policy, err := clientValidation(ctx, spec, project)
			if err != nil {
				return err
			}
			proxyArgs.ServerTlsPolicy = pulumi.Sprintf(serverPolicyPath, project, spec.Names.Policy)
			validating = append(validating, pulumi.DependsOn([]pulumi.Resource{policy}))
			ctx.Export(outputAllowlist, pulumi.String(fingerprintAllowlist(spec.ClientCertificates)))
		}
		proxy, err := compute.NewTargetHttpsProxy(ctx, spec.Names.Proxy, proxyArgs, validating...)
		if err != nil {
			return err
		}
		if _, err := compute.NewGlobalForwardingRule(ctx, spec.Names.Rule, &compute.GlobalForwardingRuleArgs{
			Name:                pulumi.String(spec.Names.Rule),
			Project:             projectID,
			Target:              proxy.SelfLink,
			IpAddress:           address.Address,
			PortRange:           pulumi.String(httpsPortRange),
			LoadBalancingScheme: pulumi.String(externalManaged),
			NetworkTier:         pulumi.String(premiumTier),
		}); err != nil {
			return err
		}
		if len(spec.ClientCertificates) > 0 {
			if err := redirectToHTTPS(ctx, spec, project, address.Address); err != nil {
				return err
			}
		}
		if err := previewWildcardResources(ctx, spec, project); err != nil {
			return err
		}
		ctx.Export(outputAddress, address.Address)
		ctx.Export(outputCertificateMap, certificates.Name)
		ctx.Export(outputURLMap, routes.Name)
		ctx.Export(outputNotFound, notFound.Name)
		return nil
	}
}
