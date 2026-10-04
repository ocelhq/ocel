//go:build integration

package aws_test

import (
	"context"
	"encoding/json"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	aws "github.com/ocelhq/ocel/platform/aws/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

func TestLiveTheKeyValueStoreConformsAsEveryStoreMust(t *testing.T) {
	a := live(t)
	tiers := []environment.Tier{environment.TierProduction, environment.TierPreview}
	boot := a.emptied(t, tiers...)
	ctx := context.Background()
	for _, tier := range tiers {
		if err := boot.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: liveWriter}, nil); err != nil {
			t.Fatalf("Apply(%s) = %v", tier, err)
		}
	}
	p, err := aws.New(ctx, provider.Settings{Options: provider.Options{"region": liveRegion}})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	store := p.KeyValues()

	conformance.RunStore(t, store)

	in := keyvalue.Partition{Tier: environment.TierProduction, Root: keyvalue.RootConformance, Path: []string{"console"}}
	written := json.RawMessage(`{"features":["cache"]}`)
	if _, err := store.Write(ctx, keyvalue.Entry{Key: in.Key("shop"), Value: written}); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	deployed, err := bootstrap.CheckDeployedFor(ctx, cloudformation.NewFromConfig(a.aws), defaultNamespace, environment.TierProduction)
	if err != nil {
		t.Fatalf("reading back what the bootstrap deployed = %v", err)
	}
	out, err := dynamodb.NewFromConfig(a.aws).GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      awssdk.String(deployed.StateTable),
		ConsistentRead: awssdk.Bool(true),
		Key: map[string]ddbtypes.AttributeValue{
			"pk": &ddbtypes.AttributeValueMemberS{Value: awsports.PartitionKey(in)},
			"sk": &ddbtypes.AttributeValueMemberS{Value: "shop#"},
		},
	})
	if err != nil {
		t.Fatalf("GetItem() = %v", err)
	}
	value, ok := out.Item["value"].(*ddbtypes.AttributeValueMemberS)
	if !ok || value.Value != string(written) {
		t.Errorf("the item holds value %#v, want the JSON written as a string, which the DynamoDB console shows as written", out.Item["value"])
	}
}
