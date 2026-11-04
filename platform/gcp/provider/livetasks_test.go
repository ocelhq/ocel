//go:build integration

package gcp_test

import (
	"context"
	"os"
	"slices"
	"strconv"
	"testing"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

func workloadClients(t *testing.T) *ports.Clients {
	t.Helper()
	clients := &ports.Clients{Namespace: liveNames(t).Namespace(), Project: liveProject(), Region: liveRegion(), Endpoint: endpoint(), TagEndpoint: os.Getenv("OCEL_FLOCI_FIRESTORE_ENDPOINT")}
	clients.Records = ports.KeyValues{Clients: clients}
	return clients
}

func projectNumber(t *testing.T) int64 {
	t.Helper()
	service, err := ports.NewRESTService(context.Background(), ports.Credentials{}, cloudresourcemanager.NewService, endpoint(), "")
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
	service, err := ports.NewRESTService(context.Background(), ports.Credentials{}, iam.NewService, endpoint(), "")
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

func TestLiveTheTopicsPartGivesTheTierADelayQueueAndAPushAccount(t *testing.T) {
	p := live(t)
	tier := environment.TierProduction
	bootstrappedWith(t, p, tier, bootstrap.TopicsPart)
	names := liveNames(t)
	ctx := context.Background()

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
}
