//go:build integration

package bastion_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
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
	return bastion.Clients{ECS: ecs.NewFromConfig(cfg), IAM: iam.NewFromConfig(cfg), EC2: ec2.NewFromConfig(cfg), PollInterval: time.Second}
}

func TestABastionIsEnsuredRunAndRemovedAgainstTheEmulator(t *testing.T) {
	ctx := context.Background()
	clients := flociClients(t)
	spec := bastion.Spec{Tier: environment.TierPreview, Boundary: "arn:aws:iam::000000000000:policy/ocel-app-boundary-preview", Ports: []int{5432, 6379}}
	t.Cleanup(func() { _ = bastion.Remove(context.WithoutCancel(ctx), clients, spec.Tier) })

	ensured, err := bastion.Ensure(ctx, clients, spec)
	if err != nil {
		t.Fatalf("Ensure() = %v", err)
	}
	again, err := bastion.Ensure(ctx, clients, spec)
	if err != nil {
		t.Fatalf("second Ensure() = %v", err)
	}
	if again.TaskDefinition != ensured.TaskDefinition || again.SecurityGroup != ensured.SecurityGroup {
		t.Errorf("Ensure() = %+v then %+v, want the same bastion reused", ensured, again)
	}

	started, err := ensured.Run(ctx, clients)
	if err != nil {
		t.Fatalf("Run() = %v", err)
	}
	tasks, err := clients.ECS.ListTasks(ctx, &ecs.ListTasksInput{Cluster: aws.String(ensured.Cluster)})
	if err != nil || len(tasks.TaskArns) != 1 {
		t.Fatalf("ListTasks() = %v, %v, want the one task Run() started", tasks, err)
	}
	if started.Target == "" {
		t.Error("Run().Target is empty")
	}

	if err := bastion.Remove(ctx, clients, spec.Tier); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	described, err := clients.ECS.DescribeClusters(ctx, &ecs.DescribeClustersInput{Clusters: []string{ensured.Cluster}})
	if err != nil {
		t.Fatalf("DescribeClusters() = %v", err)
	}
	for _, cluster := range described.Clusters {
		if aws.ToString(cluster.Status) == "ACTIVE" {
			t.Errorf("cluster %s is still active after Remove()", ensured.Cluster)
		}
	}
	if _, err := clients.IAM.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(bastion.NameFor(spec.Tier))}); err == nil {
		t.Error("the task role is still there after Remove()")
	}
}
