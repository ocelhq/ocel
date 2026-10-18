package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type networkSubnet struct {
	id      string
	zone    string
	cidr    string
	primary bool
	stack   string
}

type functionNetworkCloud struct {
	*inputRecorder
	vpcCIDR   string
	subnets   []networkSubnet
	invoked   sync.Map
	described atomic.Int32
}

func (c *functionNetworkCloud) DescribeSubnets(_ context.Context, in *awsec2.DescribeSubnetsInput, _ ...func(*awsec2.Options)) (*awsec2.DescribeSubnetsOutput, error) {
	c.described.Add(1)
	if len(in.Filters) != 1 || aws.ToString(in.Filters[0].Name) != "vpc-id" || !slices.Equal(in.Filters[0].Values, []string{"vpc-123"}) {
		return nil, fmt.Errorf("DescribeSubnets filtered by %v, want the default VPC alone", in.Filters)
	}
	out := &awsec2.DescribeSubnetsOutput{}
	for _, subnet := range c.subnets {
		described := ec2types.Subnet{
			SubnetId:         aws.String(subnet.id),
			AvailabilityZone: aws.String(subnet.zone),
			CidrBlock:        aws.String(subnet.cidr),
			DefaultForAz:     aws.Bool(subnet.primary),
		}
		if subnet.stack != "" {
			described.Tags = []ec2types.Tag{{Key: aws.String("ocel:stack"), Value: aws.String(subnet.stack)}}
		}
		out.Subnets = append(out.Subnets, described)
	}
	return out, nil
}

func defaultVPCSubnets() []networkSubnet {
	return []networkSubnet{
		{id: "subnet-default-b", zone: "us-east-1b", cidr: "172.31.16.0/20", primary: true},
		{id: "subnet-default-a", zone: "us-east-1a", cidr: "172.31.0.0/20", primary: true},
		{id: "subnet-default-c", zone: "us-east-1c", cidr: "172.31.32.0/20", primary: true},
	}
}

func (c *functionNetworkCloud) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	c.invoked.Store(args.Token, true)
	switch args.Token {
	case "aws:ec2/getVpc:getVpc":
		return resource.PropertyMap{
			"id":        resource.NewStringProperty("vpc-123"),
			"cidrBlock": resource.NewStringProperty(c.vpcCIDR),
		}, nil
	case "aws:ec2/getSubnets:getSubnets":
		ids := make([]resource.PropertyValue, 0, len(c.subnets))
		for _, subnet := range c.subnets {
			ids = append(ids, resource.NewStringProperty(subnet.id))
		}
		return resource.PropertyMap{"ids": resource.NewArrayProperty(ids)}, nil
	}
	return c.inputRecorder.Call(args)
}

func functionNetworkOutputs() auto.OutputMap {
	subnets, _ := json.Marshal([]string{"subnet-private-a", "subnet-private-b"})
	return auto.OutputMap{
		"privateSubnets":        auto.OutputValue{Value: string(subnets)},
		"functionSecurityGroup": auto.OutputValue{Value: "sg-functions"},
	}
}
