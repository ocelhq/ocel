package containers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudfront"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lb"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	originTLSPort            = 443
	vpcOriginProtocolPolicy  = "http-only"
	vpcOriginTLSFloor        = "TLSv1.2"
	vpcOriginDeployTimeout   = "20m"
	vpcOriginNameSuffixBytes = 4

	cloudFrontOriginFacingPrefixList = "com.amazonaws.global.cloudfront.origin-facing"
)

func (w *work) createFrontSecurityGroup(ctx *pulumi.Context, vpc string, tags pulumi.StringMap) (*ec2.SecurityGroup, error) {
	edgeRanges, err := ec2.LookupManagedPrefixList(ctx, &ec2.LookupManagedPrefixListArgs{Name: pulumi.StringRef(cloudFrontOriginFacingPrefixList)})
	if err != nil {
		return nil, fmt.Errorf("look up the addresses CloudFront reaches an origin from: %w", err)
	}
	return ec2.NewSecurityGroup(ctx, naming.ResourceID(naming.KindService, "front", "security-group"), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(nameFor(w.tier, "front")),
		Description: pulumi.String("Ocel: the load balancer every container app in the " + string(w.tier) + " class answers behind"),
		VpcId:       pulumi.String(vpc),
		Ingress: ec2.SecurityGroupIngressArray{&ec2.SecurityGroupIngressArgs{
			Protocol:      pulumi.String("tcp"),
			FromPort:      pulumi.Int(originListenerPort),
			ToPort:        pulumi.Int(originListenerPort),
			PrefixListIds: pulumi.StringArray{pulumi.String(edgeRanges.Id)},
			Description:   pulumi.String("Ocel: only CloudFront reaches the front, through the VPC origin, and every rule behind it demands the origin secret"),
		}},
		Egress: ec2.SecurityGroupEgressArray{&ec2.SecurityGroupEgressArgs{
			Protocol: pulumi.String("-1"), FromPort: pulumi.Int(0), ToPort: pulumi.Int(0),
			CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
		}},
		Tags: tags,
	})
}

func (w *work) runPrivateFront(ctx *pulumi.Context, front *ec2.SecurityGroup, subnets []string, tags pulumi.StringMap) error {
	balancer, err := lb.NewLoadBalancer(ctx, naming.ResourceID(naming.KindService, "front"), &lb.LoadBalancerArgs{
		Name:             pulumi.String(nameFor(w.tier)),
		LoadBalancerType: pulumi.String("application"),
		Internal:         pulumi.Bool(true),
		SecurityGroups:   pulumi.StringArray{front.ID()},
		Subnets:          pulumi.ToStringArray(subnets),
		Tags:             tags,
	})
	if err != nil {
		return err
	}
	listener, err := lb.NewListener(ctx, naming.ResourceID(naming.KindService, "front", "listener"), &lb.ListenerArgs{
		LoadBalancerArn: balancer.Arn,
		Port:            pulumi.Int(originListenerPort),
		Protocol:        pulumi.String("HTTP"),
		DefaultActions: lb.ListenerDefaultActionArray{&lb.ListenerDefaultActionArgs{
			Type: pulumi.String("fixed-response"),
			FixedResponse: &lb.ListenerDefaultActionFixedResponseArgs{
				ContentType: pulumi.String(listenerDeniedContentType),
				StatusCode:  pulumi.String(listenerDeniedStatus),
				MessageBody: pulumi.String(listenerDeniedBody),
			},
		}},
		Tags: tags,
	})
	if err != nil {
		return err
	}

	suffix := make([]byte, vpcOriginNameSuffixBytes)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("name the VPC origin: %w", err)
	}
	vpcOrigin, err := cloudfront.NewVpcOrigin(ctx, naming.ResourceID(naming.KindService, "front", "vpc-origin"), &cloudfront.VpcOriginArgs{
		VpcOriginEndpointConfig: &cloudfront.VpcOriginVpcOriginEndpointConfigArgs{
			Name:                 pulumi.String(nameFor(w.tier, hex.EncodeToString(suffix))),
			Arn:                  balancer.Arn,
			HttpPort:             pulumi.Int(originListenerPort),
			HttpsPort:            pulumi.Int(originTLSPort),
			OriginProtocolPolicy: pulumi.String(vpcOriginProtocolPolicy),
			OriginSslProtocols: &cloudfront.VpcOriginVpcOriginEndpointConfigOriginSslProtocolsArgs{
				Items:    pulumi.StringArray{pulumi.String(vpcOriginTLSFloor)},
				Quantity: pulumi.Int(1),
			},
		},
		Timeouts: &cloudfront.VpcOriginTimeoutsArgs{
			Create: pulumi.String(vpcOriginDeployTimeout),
			Update: pulumi.String(vpcOriginDeployTimeout),
			Delete: pulumi.String(vpcOriginDeployTimeout),
		},
		Tags: tags,
	}, pulumi.DependsOn([]pulumi.Resource{listener}), pulumi.IgnoreChanges([]string{"vpcOriginEndpointConfig.name"}))
	if err != nil {
		return err
	}

	ctx.Export(outputKeyListener, listener.Arn)
	ctx.Export(outputKeyOriginHost, balancer.DnsName)
	ctx.Export(outputKeyVPCOrigin, vpcOrigin.ID().ToStringOutput())
	return nil
}
