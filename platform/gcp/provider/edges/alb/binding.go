package alb

import (
	"maps"
	"slices"

	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/certificatemanager"
	"github.com/pulumi/pulumi-gcp/sdk/v9/go/gcp/compute"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/transform"
)

type bindingSpec struct {
	Namespace      string
	Region         string
	Slug           string
	Tier           environment.Tier
	CertificateMap string
	Hosts          map[string]Host
	Patches        map[string]transform.Result
}

func bindingProgram(spec bindingSpec) (Program, error) {
	patches, err := newAppPatches(spec)
	if err != nil {
		return Program{}, err
	}
	return Program{Patches: patches.applied, Run: func(ctx *pulumi.Context, in string) error {
		project := pulumi.String(in)
		for _, hostname := range slices.Sorted(maps.Keys(spec.Hosts)) {
			host := spec.Hosts[hostname]
			if host.Certificate != "" {
				entry, err := entryName(spec.Namespace, spec.Slug, spec.Tier, hostname)
				if err != nil {
					return err
				}
				args := &certificatemanager.CertificateMapEntryArgs{
					Name:         pulumi.String(entry),
					Project:      project,
					Map:          pulumi.String(spec.CertificateMap),
					Hostname:     pulumi.String(hostname),
					Certificates: pulumi.StringArray{pulumi.String(host.Certificate)},
				}
				if labels := patches.labelsOf(host.App); labels != nil {
					args.Labels = labels
				}
				if _, err := certificatemanager.NewCertificateMapEntry(ctx, entry, args); err != nil {
					return err
				}
			}
			if host.Service == "" {
				continue
			}
			neg, err := negName(spec.Namespace, spec.Slug, spec.Tier, hostname)
			if err != nil {
				return err
			}
			if err := servingBackend(ctx, project, spec.Region, neg, host, newCDNPolicy(), responseHeaders(spec.Tier, patches.headersOf(host.App))); err != nil {
				return err
			}
		}
		return nil
	}}, nil
}

func servingBackend(ctx *pulumi.Context, project pulumi.String, region, neg string, host Host, cdn *compute.BackendServiceCdnPolicyArgs, headers pulumi.StringArray) error {
	group, err := compute.NewRegionNetworkEndpointGroup(ctx, neg, &compute.RegionNetworkEndpointGroupArgs{
		Name:                pulumi.String(neg),
		Project:             project,
		Region:              pulumi.String(region),
		NetworkEndpointType: pulumi.String(serverlessNEG),
		CloudRun: &compute.RegionNetworkEndpointGroupCloudRunArgs{
			Service: pulumi.String(host.Service),
			Tag:     pulumi.StringPtrFromPtr(omitEmpty(host.Tag)),
		},
	})
	if err != nil {
		return err
	}
	args := &compute.BackendServiceArgs{
		Name:                pulumi.String(host.Backend),
		Project:             project,
		Protocol:            pulumi.String("HTTPS"),
		LoadBalancingScheme: pulumi.String(externalManaged),
		EnableCdn:           pulumi.Bool(cdn != nil),
		Backends: compute.BackendServiceBackendArray{
			&compute.BackendServiceBackendArgs{Group: group.SelfLink},
		},
	}
	if cdn != nil {
		args.CdnPolicy = cdn
	}
	if len(headers) > 0 {
		args.CustomResponseHeaders = headers
	}
	_, err = compute.NewBackendService(ctx, host.Backend, args)
	return err
}

func omitEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

const noindexHeader = "X-Robots-Tag: noindex"

func responseHeadersOf(tier environment.Tier) pulumi.StringArray {
	if tier != environment.TierPreview {
		return nil
	}
	return pulumi.StringArray{pulumi.String(noindexHeader)}
}
