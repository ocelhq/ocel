//go:build integration

package gcp_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func workloadClients(t *testing.T) *ports.Clients {
	t.Helper()
	return &ports.Clients{Namespace: liveNames(t).Namespace(), Project: liveProject(), Region: liveRegion(), Endpoint: endpoint()}
}

func projectNumber(t *testing.T) int64 {
	t.Helper()
	service, err := cloudresourcemanager.NewService(context.Background(), ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatal(err)
	}
	project, err := service.Projects.Get(liveProject()).Context(context.Background()).Do()
	if err != nil {
		t.Fatalf("read the project number: %v", err)
	}
	return project.ProjectNumber
}

func agentOf(t *testing.T, domain string) string {
	t.Helper()
	return "serviceAccount:service-" + strconv.FormatInt(projectNumber(t), 10) + domain
}

func accountGrants(t *testing.T, email, role, member string) bool {
	t.Helper()
	service, err := iam.NewService(context.Background(), ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := service.Projects.ServiceAccounts.GetIamPolicy("projects/" + liveProject() + "/serviceAccounts/" + email).Context(context.Background()).Do()
	if err != nil {
		t.Fatalf("read who may act as %s: %v", email, err)
	}
	return slices.ContainsFunc(policy.Bindings, func(binding *iam.Binding) bool {
		return binding.Role == role && slices.Contains(binding.Members, member)
	})
}

func projectGrants(t *testing.T, role, member, database string) bool {
	t.Helper()
	service, err := cloudresourcemanager.NewService(context.Background(), ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := service.Projects.GetIamPolicy(liveProject(), &cloudresourcemanager.GetIamPolicyRequest{
		Options: &cloudresourcemanager.GetPolicyOptions{RequestedPolicyVersion: 3},
	}).Context(context.Background()).Do()
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(policy.Bindings, func(binding *cloudresourcemanager.Binding) bool {
		return binding.Role == role && slices.Contains(binding.Members, member) &&
			binding.Condition != nil && strings.Contains(binding.Condition.Expression, "/databases/"+database)
	})
}

func TestLiveTheTasksFeatureGivesTheTierADelayQueueAPushAccountAndTheGrantsTheyNeed(t *testing.T) {
	p := live(t)
	tier := environment.TierProduction
	bootstrap := bootstrapped(t, p, tier, gcp.TasksFeature)
	names := liveNames(t)
	ctx := context.Background()

	described, err := bootstrap.Describe(ctx, tier)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(described.Stacks, func(stack provider.BootstrapStack) bool {
		return stack.Feature == gcp.TasksFeature && stack.Present
	}) {
		t.Errorf("Describe().Stacks = %+v, want the %s feature installed", described.Stacks, gcp.TasksFeature)
	}

	tierAccount := "serviceAccount:ocel-" + string(tier) + "@" + liveProject() + ".iam.gserviceaccount.com"
	tasksClient, err := workloadClients(t).CloudTasks()
	if err != nil {
		t.Fatal(err)
	}
	queue := names.DelayQueuePath(liveRegion(), tier)
	if _, err := tasksClient.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: queue}); err != nil {
		t.Fatalf("GetQueue(%s) = %v, want the tier's delay queue made", queue, err)
	}
	policy, err := tasksClient.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: queue})
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(policy.GetBindings(), func(binding *iampb.Binding) bool {
		return slices.Contains(binding.GetMembers(), tierAccount)
	}) {
		t.Errorf("the delay queue grants %v to %s, want none: apps are granted the queue as they deploy", policy.GetBindings(), tierAccount)
	}

	if !accountGrants(t, names.PushAccountEmail(tier), "roles/iam.serviceAccountTokenCreator", agentOf(t, "@gcp-sa-pubsub.iam.gserviceaccount.com")) {
		t.Error("Pub/Sub may not sign a push as the push account, so no push would reach a worker")
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, Remove: []string{gcp.TasksFeature}, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() removing %s = %v", gcp.TasksFeature, err)
	}
	accounts, err := iam.NewService(ctx, ports.EmulatorREST(endpoint())...)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Projects.ServiceAccounts.Get("projects/" + liveProject() + "/serviceAccounts/" + names.PushAccountEmail(tier)).Context(ctx).Do(); err == nil {
		t.Error("the push account outlived the feature that made it")
	}
	if _, err := tasksClient.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: queue}); status.Code(err) == codes.NotFound {
		t.Error("the delay queue was deleted, and Cloud Tasks holds a deleted queue's name for 7 days: a feature added again within them could not make it")
	}
}
