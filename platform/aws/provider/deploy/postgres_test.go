package deploy

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

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

type clusterRecorder struct {
	mu            sync.Mutex
	inputs        resource.PropertyMap
	ignoreChanges []string
}

func (r *clusterRecorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	if args.TypeToken == component.TokenRDSCluster {
		r.mu.Lock()
		r.inputs = args.Inputs.Copy()
		r.ignoreChanges = args.RegisterRPC.GetIgnoreChanges()
		r.mu.Unlock()
	}
	return standInCloud{}.NewResource(args)
}

func (r *clusterRecorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return standInCloud{}.Call(args)
}

func provisionPostgresIn(t *testing.T, tier environment.Tier) *clusterRecorder {
	t.Helper()
	recorder := &clusterRecorder{}
	engine := &mockedEngine{outputs: provisionedOutputs(), mocks: recorder}
	if _, err := conformingStacks(engine).Provision(context.Background(), provider.StackSpec{
		Ref:       provider.StackRef{Project: "conformance", Tier: tier, Name: naming.InfraStack("conformance")},
		Kind:      provider.StackInfra,
		Recorded:  true,
		Resources: []provider.Resource{{Name: "c-postgres", Declared: "c-postgres", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if recorder.inputs == nil {
		t.Fatal("Provision() declared no Aurora cluster")
	}
	return recorder
}

func assertRDSAcceptsSnapshotIdentifier(t *testing.T, id string) {
	t.Helper()
	if id == "" || len(id) > 255 {
		t.Fatalf("final snapshot identifier %q is %d characters, want 1 to 255", id, len(id))
	}
	if first := id[0]; first < 'a' || first > 'z' {
		t.Errorf("final snapshot identifier %q starts with %q, and RDS wants a letter first", id, first)
	}
	if strings.Contains(id, "--") || strings.HasSuffix(id, "-") {
		t.Errorf("final snapshot identifier %q has two hyphens in a row or ends with one, which RDS rejects", id)
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			t.Errorf("final snapshot identifier %q holds %q, and RDS accepts only letters, digits and hyphens", id, r)
		}
	}
}

func TestAProductionPostgresRefusesDeletionAndKeepsAFinalSnapshotWhenDeleted(t *testing.T) {
	t.Parallel()

	cluster := provisionPostgresIn(t, environment.TierProduction)

	if !cluster.inputs["deletionProtection"].BoolValue() {
		t.Error("deletionProtection = false, want true: one destroy of production would delete its data")
	}
	if cluster.inputs["skipFinalSnapshot"].BoolValue() {
		t.Error("skipFinalSnapshot = true, want false: a deleted production database leaves nothing to restore from")
	}
	var id string
	if declared := cluster.inputs["finalSnapshotIdentifier"]; declared.IsString() {
		id = declared.StringValue()
	}
	assertRDSAcceptsSnapshotIdentifier(t, id)
	if fence := naming.AppFence(testNamespace); !strings.HasPrefix(id, fence) {
		t.Errorf("final snapshot identifier %q is outside the %s fence the deploy credential may snapshot into", id, fence)
	}
	if !slices.Contains(cluster.ignoreChanges, "finalSnapshotIdentifier") {
		t.Errorf("ignoreChanges = %v, want finalSnapshotIdentifier kept at the value the cluster was created with, or every deploy plans an update to it", cluster.ignoreChanges)
	}
}

func TestAPreviewPostgresIsDeletedWithoutProtectionOrAFinalSnapshot(t *testing.T) {
	t.Parallel()

	cluster := provisionPostgresIn(t, environment.TierPreview)

	if cluster.inputs["deletionProtection"].BoolValue() {
		t.Error("deletionProtection = true, want false: a preview is torn down when its pull request closes")
	}
	if !cluster.inputs["skipFinalSnapshot"].BoolValue() {
		t.Error("skipFinalSnapshot = false, want true: a preview's teardown leaves nothing behind")
	}
	if id, set := cluster.inputs["finalSnapshotIdentifier"]; set && id.IsString() && id.StringValue() != "" {
		t.Errorf("finalSnapshotIdentifier = %q, want none for a preview", id.StringValue())
	}
}
