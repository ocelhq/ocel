//go:build integration

package bastion_test

import (
	"context"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

func flociClients(t *testing.T) bastion.Clients {
	t.Helper()
	endpoint := os.Getenv("OCEL_FLOCI_ENDPOINT")
	if endpoint == "" {
		t.Fatal("no floci emulator in the environment; run under `scripts/floci.sh run <name> -- go test -tags integration ./...`")
	}
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "us-east-1")
	cfg, err := sdkconfig.Control(context.Background(), "us-east-1")
	if err != nil {
		t.Fatalf("sdkconfig.Control() against %s = %v", endpoint, err)
	}
	return bastion.NewClients(cfg)
}

func TestABastionIsReconciledRunAndRemovedAgainstTheEmulator(t *testing.T) {
	ctx := context.Background()
	clients := flociClients(t)
	spec := bastion.Spec{Tier: environment.TierPreview, Boundary: "arn:aws:iam::000000000000:policy/ocel-app-boundary-preview", Ports: []int{5432, 6379}}
	t.Cleanup(func() { _ = bastion.Remove(context.WithoutCancel(ctx), clients, spec.Tier) })

	reconciled, err := bastion.Reconcile(ctx, clients, spec)
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	again, err := bastion.Reconcile(ctx, clients, spec)
	if err != nil {
		t.Fatalf("second Reconcile() = %v", err)
	}
	if again.TaskDefinition != reconciled.TaskDefinition || again.SecurityGroup != reconciled.SecurityGroup {
		t.Errorf("Reconcile() = %+v then %+v, want the same bastion reused", reconciled, again)
	}

	started, err := reconciled.Run(ctx, clients)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	tasks, err := clients.ECS.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(reconciled.Cluster)})
	if err != nil || len(tasks.TaskArns) != 1 {
		t.Fatalf("ListTasks() = %v, %v, want the one task Run() started", tasks, err)
	}
	if started.ManagedNode == "" {
		t.Error("Run().ManagedNode is empty")
	}

	if err := bastion.Remove(ctx, clients, spec.Tier); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	described, err := clients.ECS.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{reconciled.Cluster}})
	if err != nil {
		t.Fatalf("DescribeClusters() = %v", err)
	}
	for _, cluster := range described.Clusters {
		if aws.ToString(cluster.Status) == "ACTIVE" {
			t.Errorf("cluster %s is still active after Remove()", reconciled.Cluster)
		}
	}
	if _, err := clients.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(bastion.NameFor(spec.Tier))}); err == nil {
		t.Error("the task role is still there after Remove()")
	}
}
