package component

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type VPCsAPI interface {
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
}

type DefaultVPC struct {
	ID   string
	CIDR string
}

func ReadDefaultVPC(ctx context.Context, vpcs VPCsAPI, region, placed string) (DefaultVPC, error) {
	out, err := vpcs.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}},
	})
	if err != nil {
		return DefaultVPC{}, fmt.Errorf("look up the default VPC: %w", err)
	}
	if len(out.Vpcs) == 0 {
		return DefaultVPC{}, refusal.Refuse(refusal.CodeNotReady,
			"this account has no default VPC in %s, and ocel places %s in it: create it with `aws ec2 create-default-vpc --region %s` and run this again",
			region, placed, region)
	}
	return DefaultVPC{ID: aws.ToString(out.Vpcs[0].VpcId), CIDR: aws.ToString(out.Vpcs[0].CidrBlock)}, nil
}
