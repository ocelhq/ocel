package component

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocelhq/ocel/pkg/refusal"
)

type knownVPCs []ec2types.Vpc

func (k knownVPCs) DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: k}, nil
}

func TestAnAccountWithNoDefaultVPCIsRefusedNamingTheRegionWhatGoesThereAndHowToMakeIt(t *testing.T) {
	t.Parallel()

	_, err := ReadDefaultVPC(context.Background(), knownVPCs{}, "eu-west-2", "postgres db--main")
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("ReadDefaultVPC() = %v, want a not-ready refusal before any stack runs", err)
	}
	for _, want := range []string{"eu-west-2", "postgres db--main", "aws ec2 create-default-vpc --region eu-west-2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}

	found, err := ReadDefaultVPC(context.Background(), knownVPCs{{VpcId: aws.String("vpc-1"), CidrBlock: aws.String("172.31.0.0/16")}}, "eu-west-2", "postgres db--main")
	if err != nil || found != (DefaultVPC{ID: "vpc-1", CIDR: "172.31.0.0/16"}) {
		t.Errorf("ReadDefaultVPC() = %+v, %v, want the default VPC", found, err)
	}
}
