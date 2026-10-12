package deploy

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func lookupDefaultSubnets(ctx *pulumi.Context, vpcID string) ([]string, error) {
	subnets, err := ec2.GetSubnets(ctx, &ec2.GetSubnetsArgs{
		Filters: []ec2.GetSubnetsFilter{
			{Name: "vpc-id", Values: []string{vpcID}},
			{Name: "default-for-az", Values: []string{"true"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("look up default VPC subnets: %w", err)
	}
	return subnets.Ids, nil
}
