package deploy

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/resources/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func TestTearingDownProductionPlansItsPostgresClusterAsDisableThenDelete(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: auto.OutputMap{}, previewed: []provider.Change{
		{Kind: component.TokenRDSCluster, Name: "c-postgres"},
		{Kind: component.TokenRDSClusterMember, Name: "c-postgres-instance"},
	}}
	plan, err := conformingStacks(engine).PlanDestroy(context.Background(), conformanceInfra, nil)
	if err != nil {
		t.Fatalf("PlanDestroy() = %v", err)
	}
	actions := map[string]provider.ChangeAction{}
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			actions[change.Kind] = change.Action
		}
	}
	if got := actions[component.TokenRDSCluster]; got != provider.ActionDisableThenDelete {
		t.Errorf("the plan does %q to the production cluster, want %q: the teardown disables its deletion protection first", got, provider.ActionDisableThenDelete)
	}
	if got := actions[component.TokenRDSClusterMember]; got != provider.ActionDelete {
		t.Errorf("the plan does %q to the cluster's instance, want %q", got, provider.ActionDelete)
	}
}

func TestTearingDownAPreviewPlansItsPostgresClusterAsAPlainDelete(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: auto.OutputMap{}, previewed: []provider.Change{{Kind: component.TokenRDSCluster, Name: "c-postgres"}}}
	ref := provider.StackRef{Project: "conformance", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}
	plan, err := conformingStacks(engine).PlanDestroy(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("PlanDestroy() = %v", err)
	}
	if got := plan.Groups[0].Changes[0].Action; got != provider.ActionDelete {
		t.Errorf("the plan does %q to a preview cluster, want %q", got, provider.ActionDelete)
	}
}

type fakeClusters struct {
	mu                    sync.Mutex
	engine                *mockedEngine
	missing               string
	disabled              []string
	disabledAfterTeardown []string
}

func (f *fakeClusters) ModifyDBCluster(_ context.Context, in *rds.ModifyDBClusterInput, _ ...func(*rds.Options)) (*rds.ModifyDBClusterOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := aws.ToString(in.DBClusterIdentifier)
	if id == f.missing {
		return nil, &rdstypes.DBClusterNotFoundFault{Message: aws.String("DBCluster " + id + " not found.")}
	}
	if in.DeletionProtection == nil || *in.DeletionProtection || !aws.ToBool(in.ApplyImmediately) {
		return nil, fmt.Errorf("ModifyDBCluster(%s) does not disable deletion protection immediately", id)
	}
	if len(f.engine.torn()) > 0 {
		f.disabledAfterTeardown = append(f.disabledAfterTeardown, id)
	}
	f.disabled = append(f.disabled, id)
	return &rds.ModifyDBClusterOutput{}, nil
}

func postgresOutputOf(cluster string) auto.OutputValue {
	return auto.OutputValue{Value: map[string]any{
		component.OutputKeyHost:             cluster + ".cluster-abc.eu-west-1.rds.amazonaws.com",
		component.OutputKeyPort:             float64(5432),
		postgres.OutputKeyDatabase:          "ocel",
		postgres.OutputKeyUsername:          "ocel",
		postgres.OutputKeySecretARN:         "arn:aws:secretsmanager:eu-west-1:111122223333:secret:" + cluster,
		postgres.OutputKeyClusterIdentifier: cluster,
	}}
}

func clustersStacks(engine *mockedEngine, clusters *fakeClusters) *Stacks {
	cfg := conformingConfig(&fakeArtifactStore{})
	cfg.Clusters = clusters
	return stacksWith(cfg, engine)
}

func TestDestroyingProductionDisablesEachPostgresClustersDeletionProtectionBeforeTheTeardown(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: auto.OutputMap{
		"c-postgres": postgresOutputOf("ocel-app-conformance-main-abc"),
		"c-reports":  postgresOutputOf("ocel-app-conformance-reports-def"),
		"c-bucket":   auto.OutputValue{Value: map[string]any{bucket.OutputKeyBucket: "shop-prod-uploads"}},
	}}
	clusters := &fakeClusters{engine: engine}
	if err := clustersStacks(engine, clusters).Destroy(context.Background(), provider.StackDestroy{Ref: conformanceInfra}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	want := []string{"ocel-app-conformance-main-abc", "ocel-app-conformance-reports-def"}
	if got := slices.Sorted(slices.Values(clusters.disabled)); !slices.Equal(got, want) {
		t.Errorf("Destroy() disabled deletion protection on %v, want %v", got, want)
	}
	if len(clusters.disabledAfterTeardown) > 0 {
		t.Errorf("Destroy() tore the stack down before disabling protection on %v, so AWS refuses to delete them", clusters.disabledAfterTeardown)
	}
	if len(engine.torn()) != 1 {
		t.Errorf("Destroy() tore down %v, want the stack torn down once protection is disabled", engine.torn())
	}
}

func TestDestroyingProductionGoesOnWhenAPostgresClusterIsAlreadyGone(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: auto.OutputMap{"c-postgres": postgresOutputOf("ocel-app-conformance-main-abc")}}
	clusters := &fakeClusters{engine: engine, missing: "ocel-app-conformance-main-abc"}
	if err := clustersStacks(engine, clusters).Destroy(context.Background(), provider.StackDestroy{Ref: conformanceInfra}, nil); err != nil {
		t.Fatalf("Destroy() = %v, want a cluster already deleted to leave nothing to disable", err)
	}
	if len(engine.torn()) != 1 {
		t.Errorf("Destroy() tore down %v, want the stack torn down", engine.torn())
	}
}

func TestDestroyingAPreviewLeavesPostgresDeletionProtectionAlone(t *testing.T) {
	t.Parallel()

	engine := &mockedEngine{outputs: auto.OutputMap{"c-postgres": postgresOutputOf("ocel-app-conformance-pr-7-main-abc")}}
	clusters := &fakeClusters{engine: engine}
	ref := provider.StackRef{Project: "conformance", Tier: environment.TierPreview, Name: naming.InfraStack("pr-7")}
	if err := clustersStacks(engine, clusters).Destroy(context.Background(), provider.StackDestroy{Ref: ref}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(clusters.disabled) > 0 {
		t.Errorf("Destroy() of a preview called ModifyDBCluster on %v, want no call: a preview cluster was never protected", clusters.disabled)
	}
}
