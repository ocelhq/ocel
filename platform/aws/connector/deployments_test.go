package main

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	kit "github.com/ocelhq/ocel/pkg/providerkit/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

type countingStacks struct {
	describes int
	table     string
}

func (c *countingStacks) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	c.describes++
	if c.table == "" {
		return &cloudformation.DescribeStacksOutput{}, nil
	}
	return &cloudformation.DescribeStacksOutput{Stacks: []cfntypes.Stack{{
		StackName:   in.StackName,
		StackStatus: cfntypes.StackStatusCreateComplete,
		Outputs:     []cfntypes.Output{{OutputKey: aws.String("StateTableName"), OutputValue: aws.String(c.table)}},
	}}}, nil
}

func TestTheConnectorRereadsTheBootstrapOnceItsMemoAges(t *testing.T) {
	t.Parallel()

	stacks := &countingStacks{}
	clock := time.Unix(1_000_000, 0)
	held := &deployments{
		namespace: bootstrap.Namespace("ocel"),
		stacks:    stacks,
		now:       func() time.Time { return clock },
		read:      map[kit.Class]readDeployment{},
	}
	ctx := context.Background()

	if _, err := held.Table(ctx, kit.ClassProduction); err != nil {
		t.Fatalf("Table: %v", err)
	}
	if _, err := held.Table(ctx, kit.ClassProduction); err != nil {
		t.Fatalf("Table again: %v", err)
	}
	if stacks.describes != 1 {
		t.Fatalf("the bootstrap was described %d times within the memo's life, want once", stacks.describes)
	}

	stacks.table = "ocel-state"
	clock = clock.Add(deploymentsTTL)
	table, err := held.Table(ctx, kit.ClassProduction)
	if err != nil {
		t.Fatalf("Table after the memo aged: %v", err)
	}
	if table != "ocel-state" {
		t.Errorf("Table = %q after the memo aged, want the table the re-bootstrapped account now names", table)
	}
}
