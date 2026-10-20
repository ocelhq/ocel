package postgres

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

var testNetwork = component.Network{VPCID: "vpc-1", SubnetIDs: []string{"subnet-1", "subnet-2"}, Clients: "sg-vpc-clients"}

const testCluster = "ocel-app-prod-shop-prod-main-ab8633"

func testStack(project string, network component.Network) component.Stack {
	return component.Stack{Ref: provider.StackRef{Tier: environment.TierProduction}, Namespace: "ocel", Project: project, Env: "prod", Network: network}
}

func masterUserSecret(args pulumi.MockResourceArgs) resource.PropertyMap {
	if args.TypeToken != "aws:rds/cluster:Cluster" {
		return nil
	}
	return resource.PropertyMap{
		"masterUserSecrets": resource.NewArrayProperty([]resource.PropertyValue{
			resource.NewObjectProperty(resource.PropertyMap{
				"secretArn": resource.NewStringProperty("arn:aws:secretsmanager:eu-west-1:111122223333:secret:db-main"),
			}),
		}),
	}
}
