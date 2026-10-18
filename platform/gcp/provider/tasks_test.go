package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/task"
)

func recordingTopics(t *testing.T, tier environment.Tier, bindings ...provider.Binding) records.Store {
	t.Helper()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if _, err := store.Write(ctx, records.Entry{Key: stackrecords.ProjectKey(tier, "shop"), Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.Write(ctx, store, tier, "shop", naming.InfraStack("prod"), stackrecords.Stack{Kind: provider.StackInfra, Bindings: bindings}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRemovingATierWhileAnEnvironmentDeclaresATaskIsRefusedNamingIt(t *testing.T) {
	t.Parallel()

	b := composed(bootstrap.Engine{Records: recordingTopics(t, environment.TierProduction,
		provider.Binding{Type: provider.BindingBucket, Name: "uploads"},
		provider.Binding{Type: provider.BindingTask, Name: "task--resize", Properties: map[string]string{stackrecords.PropertyDeclared: "resize"}},
	)})

	err := task.RefuseInUse(context.Background(), b, environment.TierProduction, bootstrap.PartRemoval{})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "task resize of project shop environment prod") {
		t.Fatalf("refuseTasksInUse() while a task is declared = %v, want a refusal naming the task, its project and environment", err)
	}
	if strings.Contains(refused.Message, "uploads") {
		t.Errorf("the refusal names %q, want only what runs on the part", refused.Message)
	}
	if users, err := task.ListUsers(context.Background(), b, environment.TierProduction); err != nil || !slices.Equal(users, []string{"shop"}) {
		t.Errorf("listTaskUsers() = %v, %v, want shop, whose environment runs a task", users, err)
	}
}

func TestRemovingATierIgnoresATopicOnAnotherTier(t *testing.T) {
	t.Parallel()

	b := composed(bootstrap.Engine{Clients: googletest.GrantedIAM().Open(t), Records: recordingTopics(t, environment.TierPreview,
		provider.Binding{Type: provider.BindingTopic, Name: "topic--orders", Properties: map[string]string{stackrecords.PropertyDeclared: "orders"}},
	)})
	if err := task.RefuseInUse(context.Background(), b, environment.TierProduction, bootstrap.PartRemoval{}); err != nil {
		t.Errorf("refuseTasksInUse() with a topic only on another tier = %v, want the part free", err)
	}
}

func TestTopicsAndTasksRunOnAPartADeployAdds(t *testing.T) {
	t.Parallel()

	if slices.ContainsFunc(bootstrap.CredentialParts(), func(f provider.Part) bool { return f.Name == task.PartName }) {
		t.Errorf("credentialParts() = %v, want no %q part only the bootstrap credential adds: the first deploy declaring a topic or a task adds what they run on", bootstrap.CredentialParts(), task.PartName)
	}
	names := names.New("ocel", "acme-prod")
	push := bootstrap.Resource{Kind: bootstrap.KindServiceAccount, Name: names.PushAccount(environment.TierProduction)}
	if !slices.Contains(idsOf(bootstrap.Resources(names, environment.TierProduction, false)), push.ID()) {
		t.Errorf("the base provisions %v, want %s: Pub/Sub may sign a push as it only once a bootstrap grants it", idsOf(bootstrap.Resources(names, environment.TierProduction, false)), push.ID())
	}
	queue := bootstrap.Resource{Kind: bootstrap.KindQueue, Name: names.DelayQueue(environment.TierProduction)}
	if !slices.Contains(idsOf(bootstrap.Resources(names, environment.TierProduction, true)), queue.ID()) {
		t.Errorf("the base provisions %v, want %s: a deploy grants apps the queue and may grant only on a queue that already exists", idsOf(bootstrap.Resources(names, environment.TierProduction, true)), queue.ID())
	}
}

func TestTopicsAndTasksCheckTheServicesAndPermissionsTheyAreAddedWith(t *testing.T) {
	t.Parallel()
	b := composed(bootstrap.Engine{Clients: &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}})
	tasks := task.NewPart(b, environment.TierProduction)

	if slices.ContainsFunc(tasks.Resources, func(resource provider.PartResource) bool { return resource.Kind == string(bootstrap.KindQueue) }) {
		t.Errorf("the tasks part holds %v, want no queue: the base makes the delay queue", tasks.Resources)
	}
	for _, api := range []string{"pubsub.googleapis.com", "cloudtasks.googleapis.com"} {
		if !slices.Contains(bootstrap.APIsFor(environment.TierProduction, nil), api) {
			t.Errorf("a bootstrap checks %v, want %s: the base grants Pub/Sub's agent the push account and makes the delay queue", bootstrap.APIsFor(environment.TierProduction, nil), api)
		}
	}
	if parts := bootstrap.NewPartsRole(b.Clients.Names).Permissions; slices.ContainsFunc(parts, func(permission string) bool { return strings.HasPrefix(permission, "cloudtasks.") }) {
		t.Errorf("the parts role holds %v, want no Cloud Tasks permission: granted on the whole project, it would let a deploy set who may enqueue on any queue", parts)
	}
	for _, permission := range []string{"cloudtasks.queues.create", "cloudtasks.queues.purge", "cloudtasks.queues.getIamPolicy", "cloudtasks.queues.setIamPolicy"} {
		if !slices.Contains(bootstrap.BootstrapPermissions, permission) {
			t.Errorf("a bootstrap checks %v, want %s among them: the base makes, purges and grants on the delay queue", bootstrap.BootstrapPermissions, permission)
		}
	}
	databases := bootstrap.NewPartDatabasesRole(b.Clients.Names).Permissions
	for _, permission := range []string{"datastore.databases.create", "datastore.databases.update", "datastore.operations.get", "datastore.schemas.create", "datastore.schemas.update"} {
		if !slices.Contains(databases, permission) {
			t.Errorf("the part databases role holds %v, want %s among them: a deploy makes the task database, its indexes and time to live", databases, permission)
		}
	}
	if !slices.Contains(bootstrap.RolesFor(edge.PurposeBootstrap), "roles/cloudtasks.admin") {
		t.Errorf("a bootstrap credential is granted %v, want roles/cloudtasks.admin: removing a bootstrap purges its delay queue", bootstrap.RolesFor(edge.PurposeBootstrap))
	}
	for _, role := range []string{"roles/pubsub.admin", "roles/cloudscheduler.admin"} {
		if !slices.Contains(bootstrap.RolesFor(edge.PurposeDeploy), role) {
			t.Errorf("a deploy credential is granted %v, want %s: a deploy makes and removes topics, subscriptions and schedules", bootstrap.RolesFor(edge.PurposeDeploy), role)
		}
	}
}

func TestEveryListingOfATiersRunsByTopicConsumerStatusAndTagHasItsIndex(t *testing.T) {
	t.Parallel()

	shapes := map[string]bool{}
	for _, index := range task.RunIndexes() {
		key := ""
		for _, field := range index.Fields {
			key += field.FieldPath + ":" + field.Order + field.ArrayConfig + " "
		}
		shapes[key] = true
		if index.QueryScope != "COLLECTION" {
			t.Errorf("an index on runs is scoped to %s, want each environment's own runs collection", index.QueryScope)
		}
	}
	for _, consumer := range []string{"", "consumer:ASCENDING "} {
		for _, status := range []string{"", "status:ASCENDING "} {
			for _, tags := range []string{"", "tags:CONTAINS "} {
				if want := "topic:ASCENDING " + consumer + status + tags + "createdAt:DESCENDING "; !shapes[want] {
					t.Errorf("the runs indexes are %v, want one on %s", shapes, want)
				}
			}
		}
	}
}

func TestTheDelayQueueIsMadeGrantingNoOne(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := composed(bootstrap.Engine{Clients: server.Open(t)})

	if err := b.EnsureDelayQueue(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("ensureDelayQueue() = %v", err)
	}
	if server.QueueWrites != 0 {
		t.Errorf("%d queue policies were written, want none: apps are granted the queue as they deploy", server.QueueWrites)
	}
}

func TestABootstrapChecksEveryPermissionItsFirestoreAdminCallsNeed(t *testing.T) {
	t.Parallel()

	needs := map[string]string{
		"datastore.databases.create":      "creating a Firestore database",
		"datastore.databases.getMetadata": "reading a Firestore database",
		"datastore.databases.update":      "protecting a Firestore database",
		"datastore.databases.delete":      "deleting a Firestore database",
		"datastore.locations.list":        "asking which locations Firestore serves the project from",
		"datastore.operations.get":        "polling a Firestore operation",
		"datastore.schemas.create":        "creating a Firestore index",
		"datastore.schemas.list":          "listing Firestore indexes",
		"datastore.schemas.update":        "exempting a Firestore field from indexing",
	}
	for permission, call := range needs {
		if !slices.Contains(bootstrap.PermissionsFor(nil), permission) {
			t.Errorf("a bootstrap checks %v, want %s among them: %s would fail mid-apply", bootstrap.PermissionsFor(nil), permission, call)
		}
	}
}

func TestABootstrapChecksFirestoreIndexPermissionsByTheirCurrentNames(t *testing.T) {
	t.Parallel()

	for _, permission := range slices.Concat(bootstrap.PermissionsFor(nil), bootstrap.NewPartDatabasesRole(names.New("ocel", "acme-prod")).Permissions) {
		if strings.HasPrefix(permission, "datastore.indexes.") {
			t.Errorf("a bootstrap or a deploy is granted %s, want datastore.schemas.*: Google's role reference lists only the schemas names", permission)
		}
	}
}

func addingTasks(t *testing.T) (*googletest.IAM, bootstrap.Engine) {
	t.Helper()
	server := googletest.GrantedIAM()
	return server, composed(bootstrap.Engine{Clients: server.Open(t), Records: fake.NewKeyValues()})
}

func TestTheFirstTopicOrTaskAddsTheTasksPartItRecordsAndMakesNoQueue(t *testing.T) {
	t.Parallel()
	server, b := addingTasks(t)
	ctx := context.Background()
	tier := environment.TierProduction

	if err := b.EnsurePart(ctx, tier, "shop", task.NewPart(b, tier)); err != nil {
		t.Fatalf("ensurePart(tasks) = %v", err)
	}
	if len(server.QueuesMade) != 0 {
		t.Errorf("adding the tasks part made queues %v, want none: the base makes the delay queue", server.QueuesMade)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.Records, tier, task.PartName)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" {
		t.Errorf("the part record = %+v, %t, %v, want it ready and added by shop", recorded, present, err)
	}

}

func TestADryDeployDeclaringATaskPlansTheTasksPartAndMakesNothing(t *testing.T) {
	var planned []provider.ChangeGroup
	pre := declaringATask()
	pre.Dry = true
	pre.AddToPlan = func(groups ...provider.ChangeGroup) { planned = append(planned, groups...) }

	if err := failingOnAnyCall(t).PreflightDeploy(context.Background(), pre); err != nil {
		t.Fatalf("PreflightDeploy() = %v", err)
	}
	if len(planned) != 1 || planned[0].Part != task.PartName || planned[0].Action != provider.ActionCreate {
		t.Errorf("a dry deploy declaring a task planned %+v, want the %s part created", planned, task.PartName)
	}
}

func TestRemovingABootstrapPurgesItsDelayQueueAndKeepsIt(t *testing.T) {
	t.Parallel()
	server := googletest.GrantedIAM()
	b := composed(bootstrap.Engine{Clients: server.Open(t)})
	ctx := context.Background()
	tier := environment.TierProduction
	read := bootstrap.Survey{Tier: tier, Names: b.Clients.Names, Emulated: true}
	queue := bootstrap.Resource{Kind: bootstrap.KindQueue, Name: b.Clients.DelayQueue(tier)}
	read.Held = map[string]bool{queue.ID(): true}

	if taking := bootstrap.Removing(read, queue); taking.Action != provider.ActionUpdate || taking.Reason != bootstrap.ReasonQueuePurged {
		t.Errorf("removing the delay queue plans %s (%s), want it purged and kept", taking.Action, taking.Reason)
	}
	if _, err := queue.KindOps().Take(ctx, b, read, queue); err != nil {
		t.Fatalf("take(queue) = %v", err)
	}
	if !slices.Equal(server.QueuesPurged, []string{b.DelayQueuePath(tier)}) {
		t.Errorf("removing the bootstrap purged %v, want its delay queue: Cloud Tasks holds a deleted queue's name for days", server.QueuesPurged)
	}
}
