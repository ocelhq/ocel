package pulumi

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
)

const stackURN = "urn:pulumi:prod--infra::ocel-shop::"

func deployment(t *testing.T, version int, resources string) apitype.UntypedDeployment {
	t.Helper()

	raw := json.RawMessage(`{"manifest":{"time":"2026-10-19T00:00:00Z","magic":"","version":"v3.259.0"},"resources":` + resources + `}`)
	if !json.Valid(raw) {
		t.Fatalf("the canned deployment is not JSON: %s", raw)
	}
	return apitype.UntypedDeployment{Version: version, Deployment: raw}
}

func TestExportListsEveryCustomResourceInTheStackState(t *testing.T) {
	t.Parallel()

	exported, err := decodeExportedResources(deployment(t, 3, `[
		{"urn":"`+stackURN+`aws:s3/bucketV2:BucketV2::uploads","custom":true,"id":"shop-prod-uploads","type":"aws:s3/bucketV2:BucketV2"},
		{"urn":"`+stackURN+`ocel:index:Postgres$aws:rds/cluster:Cluster::db","custom":true,"id":"shop-prod-db","type":"aws:rds/cluster:Cluster"}
	]`))
	if err != nil {
		t.Fatalf("decodeExportedResources() = %v", err)
	}

	want := []ExportedResource{
		{
			Type: "aws:s3/bucketV2:BucketV2",
			ID:   "shop-prod-uploads",
		},
		{
			Type: "aws:rds/cluster:Cluster",
			ID:   "shop-prod-db",
		},
	}
	if !slices.Equal(exported, want) {
		t.Errorf("decodeExportedResources() = %+v, want %+v", exported, want)
	}
}

func TestExportLeavesOutProvidersComponentsAndResourcesPendingDeletion(t *testing.T) {
	t.Parallel()

	exported, err := decodeExportedResources(deployment(t, 3, `[
		{"urn":"`+stackURN+`pulumi:pulumi:Stack::ocel-shop-prod--infra","custom":false,"type":"pulumi:pulumi:Stack"},
		{"urn":"`+stackURN+`pulumi:providers:aws::default","custom":true,"id":"0b5c-provider","type":"pulumi:providers:aws"},
		{"urn":"`+stackURN+`ocel:index:Postgres::db","custom":false,"type":"ocel:index:Postgres"},
		{"urn":"`+stackURN+`aws:s3/bucketV2:BucketV2::old-uploads","custom":true,"id":"shop-prod-old","type":"aws:s3/bucketV2:BucketV2","delete":true},
		{"urn":"`+stackURN+`aws:s3/bucketV2:BucketV2::unborn","custom":true,"type":"aws:s3/bucketV2:BucketV2"},
		{"urn":"`+stackURN+`aws:s3/bucketV2:BucketV2::uploads","custom":true,"id":"shop-prod-uploads","type":"aws:s3/bucketV2:BucketV2"}
	]`))
	if err != nil {
		t.Fatalf("decodeExportedResources() = %v", err)
	}

	want := []ExportedResource{{
		Type: "aws:s3/bucketV2:BucketV2",
		ID:   "shop-prod-uploads",
	}}
	if !slices.Equal(exported, want) {
		t.Errorf("decodeExportedResources() = %+v, want only the live custom resource %+v", exported, want)
	}
}

func TestExportLeavesOutAResourceTheStackOnlyReads(t *testing.T) {
	t.Parallel()

	exported, err := decodeExportedResources(deployment(t, 3, `[
		{"urn":"`+stackURN+`aws:ec2/vpc:Vpc::default","custom":true,"id":"vpc-0abc","type":"aws:ec2/vpc:Vpc","external":true}
	]`))
	if err != nil {
		t.Fatalf("decodeExportedResources() = %v", err)
	}
	if len(exported) != 0 {
		t.Errorf("decodeExportedResources() = %+v, want nothing for a resource another owner created", exported)
	}
}

func TestExportRefusesAStateOfAVersionItCannotRead(t *testing.T) {
	t.Parallel()

	_, err := decodeExportedResources(deployment(t, 4, `[]`))
	if err == nil {
		t.Fatal("decodeExportedResources() of a version 4 state = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "version 4") {
		t.Errorf("decodeExportedResources() = %q, want it to name the version it cannot read", err)
	}
}

func TestExportOfAStackWithNoResourcesIsEmpty(t *testing.T) {
	t.Parallel()

	exported, err := decodeExportedResources(apitype.UntypedDeployment{Version: 3, Deployment: json.RawMessage(`{"manifest":{"time":"2026-10-19T00:00:00Z","magic":"","version":"v3.259.0"}}`)})
	if err != nil {
		t.Fatalf("decodeExportedResources() = %v", err)
	}
	if len(exported) != 0 {
		t.Errorf("decodeExportedResources() = %+v, want nothing", exported)
	}
}
