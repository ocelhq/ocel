package cloudfront

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

func TestProjectResourcesNameTheCloudFrontDistributionRecordedForTheProject(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ns := account.Namespace("ocel2")
	tier := environment.TierProduction
	recorded := distributionRecords{keyValues: store, tier: tier}
	if err := recorded.record(ctx, must(distributionName(ns, "shop", tier)), front{id: "E2QWRUHAPOMQZL", domainName: "d111111abcdef8.cloudfront.net"}); err != nil {
		t.Fatal(err)
	}
	if err := recorded.record(ctx, must(distributionName(ns, "blog", tier)), front{id: "E3OTHERPROJECT", domainName: "d222.cloudfront.net"}); err != nil {
		t.Fatal(err)
	}

	listed, err := ReadProjectResources(ctx, store, ns, "shop", tier)
	if err != nil {
		t.Fatalf("ReadProjectResources() = %v", err)
	}

	want := []provider.PhysicalResource{{Kind: "AWS::CloudFront::Distribution", ID: "E2QWRUHAPOMQZL", Location: "global"}}
	if !slices.Equal(listed, want) {
		t.Errorf("ReadProjectResources() = %+v, want %+v", listed, want)
	}
}

func TestProjectResourcesNameNoDistributionForAProjectCloudFrontNeverServed(t *testing.T) {
	t.Parallel()

	listed, err := ReadProjectResources(context.Background(), fake.NewKeyValues(), account.Namespace("ocel2"), "shop", environment.TierPreview)
	if err != nil || len(listed) != 0 {
		t.Errorf("ReadProjectResources() = %+v, %v, want none", listed, err)
	}
}
