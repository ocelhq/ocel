package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func recordingTopics(t *testing.T, tier environment.Tier, bindings ...provider.Binding) keyvalue.Store {
	t.Helper()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if _, err := store.Write(ctx, keyvalue.Entry{Key: stackrecords.ProjectKey(tier, "shop"), Value: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.Write(ctx, store, tier, "shop", naming.InfraStack("prod"), stackrecords.Stack{Kind: provider.StackInfra, Bindings: bindings}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRemovingATierWhileAnEnvironmentDeclaresATaskIsRefusedNamingIt(t *testing.T) {
	t.Parallel()

	b := bootstrap{records: recordingTopics(t, environment.TierProduction,
		provider.Binding{Type: provider.BindingBucket, Name: "uploads"},
		provider.Binding{Type: provider.BindingTask, Name: "task--resize", Properties: map[string]string{stackrecords.PropertyDeclared: "resize"}},
	)}

	err := b.refuseTasksInUse(context.Background(), environment.TierProduction, partRemoval{})
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "task resize of project shop environment prod") {
		t.Fatalf("refuseTasksInUse() while a task is declared = %v, want a refusal naming the task, its project and environment", err)
	}
	if strings.Contains(refused.Message, "uploads") {
		t.Errorf("the refusal names %q, want only what runs on the part", refused.Message)
	}
	if users, err := b.listTaskUsers(context.Background(), environment.TierProduction); err != nil || !slices.Equal(users, []string{"shop"}) {
		t.Errorf("listTaskUsers() = %v, %v, want shop, whose environment runs a task", users, err)
	}
}

func TestRemovingATierIgnoresATopicOnAnotherTier(t *testing.T) {
	t.Parallel()

	b := bootstrap{clients: grantedIAM().open(t), records: recordingTopics(t, environment.TierPreview,
		provider.Binding{Type: provider.BindingTopic, Name: "topic--orders", Properties: map[string]string{stackrecords.PropertyDeclared: "orders"}},
	)}
	if err := b.refuseTasksInUse(context.Background(), environment.TierProduction, partRemoval{}); err != nil {
		t.Errorf("refuseTasksInUse() with a topic only on another tier = %v, want the part free", err)
	}
}

func TestTopicsAndTasksRunOnAPartADeployAdds(t *testing.T) {
	t.Parallel()

	if slices.ContainsFunc(credentialParts(), func(f provider.Part) bool { return f.Name == tasksPart }) {
		t.Errorf("credentialParts() = %v, want no %q part only the bootstrap credential adds: the first deploy declaring a topic or a task adds what they run on", credentialParts(), tasksPart)
	}
	names := Names{namespace: "ocel", project: "acme-prod"}
	push := item{Kind: KindServiceAccount, Name: names.PushAccount(environment.TierProduction)}
	if !slices.Contains(idsOf(bootstrapItems(names, environment.TierProduction, false)), push.ID()) {
		t.Errorf("the base provisions %v, want %s: Pub/Sub may sign a push as it only once a bootstrap grants it", idsOf(bootstrapItems(names, environment.TierProduction, false)), push.ID())
	}
	queue := item{Kind: KindQueue, Name: names.DelayQueue(environment.TierProduction)}
	if !slices.Contains(idsOf(bootstrapItems(names, environment.TierProduction, true)), queue.ID()) {
		t.Errorf("the base provisions %v, want %s: a deploy grants apps the queue and may grant only on a queue that already exists", idsOf(bootstrapItems(names, environment.TierProduction, true)), queue.ID())
	}
}

func TestTopicsAndTasksCheckTheServicesAndPermissionsTheyAreAddedWith(t *testing.T) {
	t.Parallel()
	b := bootstrap{clients: &clients{Names: Names{namespace: "ocel", project: "acme-prod"}, region: "europe-west1"}}
	tasks := b.tasksPart(environment.TierProduction)

	if slices.ContainsFunc(tasks.resources, func(resource provider.PartResource) bool { return resource.Kind == string(KindQueue) }) {
		t.Errorf("the tasks part holds %v, want no queue: the base makes the delay queue", tasks.resources)
	}
	for _, api := range []string{"pubsub.googleapis.com", "cloudtasks.googleapis.com"} {
		if !slices.Contains(apisFor(environment.TierProduction, nil), api) {
			t.Errorf("a bootstrap checks %v, want %s: the base grants Pub/Sub's agent the push account and makes the delay queue", apisFor(environment.TierProduction, nil), api)
		}
	}
	if parts := newPartsRole(b.clients.Names).permissions; slices.ContainsFunc(parts, func(permission string) bool { return strings.HasPrefix(permission, "cloudtasks.") }) {
		t.Errorf("the parts role holds %v, want no Cloud Tasks permission: granted on the whole project, it would let a deploy set who may enqueue on any queue", parts)
	}
	for _, permission := range []string{"cloudtasks.queues.create", "cloudtasks.queues.purge", "cloudtasks.queues.getIamPolicy", "cloudtasks.queues.setIamPolicy"} {
		if !slices.Contains(bootstrapPermissions, permission) {
			t.Errorf("a bootstrap checks %v, want %s among them: the base makes, purges and grants on the delay queue", bootstrapPermissions, permission)
		}
	}
	databases := newPartDatabasesRole(b.clients.Names).permissions
	for _, permission := range []string{"datastore.databases.create", "datastore.databases.update", "datastore.operations.get", "datastore.schemas.create", "datastore.schemas.update"} {
		if !slices.Contains(databases, permission) {
			t.Errorf("the part databases role holds %v, want %s among them: a deploy makes the task database, its indexes and time to live", databases, permission)
		}
	}
	if !slices.Contains(rolesFor(edge.PurposeBootstrap), "roles/cloudtasks.admin") {
		t.Errorf("a bootstrap credential is granted %v, want roles/cloudtasks.admin: removing a bootstrap purges its delay queue", rolesFor(edge.PurposeBootstrap))
	}
	for _, role := range []string{"roles/pubsub.admin", "roles/cloudscheduler.admin"} {
		if !slices.Contains(rolesFor(edge.PurposeDeploy), role) {
			t.Errorf("a deploy credential is granted %v, want %s: a deploy makes and removes topics, subscriptions and schedules", rolesFor(edge.PurposeDeploy), role)
		}
	}
}

func TestTheRunsOfATierAreListedByTopicStatusAndTagThroughItsIndexes(t *testing.T) {
	t.Parallel()

	shapes := map[string]bool{}
	for _, index := range runIndexes() {
		key := ""
		for _, field := range index.Fields {
			key += field.FieldPath + ":" + field.Order + field.ArrayConfig + " "
		}
		shapes[key] = true
		if index.QueryScope != "COLLECTION" {
			t.Errorf("an index on runs is scoped to %s, want each environment's own runs collection", index.QueryScope)
		}
	}
	for _, want := range []string{
		"topic:ASCENDING createdAt:DESCENDING ",
		"status:ASCENDING createdAt:DESCENDING ",
		"tags:CONTAINS createdAt:DESCENDING ",
		"topic:ASCENDING status:ASCENDING tags:CONTAINS createdAt:DESCENDING ",
		"topic:ASCENDING consumer:ASCENDING status:ASCENDING finishedAt:ASCENDING ",
	} {
		if !shapes[want] {
			t.Errorf("the runs indexes are %v, want one on %s", shapes, want)
		}
	}
}

func TestTheDelayQueueIsMadeGrantingNoOne(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	b := bootstrap{clients: server.open(t)}

	if err := b.ensureDelayQueue(context.Background(), environment.TierProduction); err != nil {
		t.Fatalf("ensureDelayQueue() = %v", err)
	}
	if server.queueWrites != 0 {
		t.Errorf("%d queue policies were written, want none: apps are granted the queue as they deploy", server.queueWrites)
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
		if !slices.Contains(permissionsFor(nil), permission) {
			t.Errorf("a bootstrap checks %v, want %s among them: %s would fail mid-apply", permissionsFor(nil), permission, call)
		}
	}
}

func TestABootstrapChecksFirestoreIndexPermissionsByTheirCurrentNames(t *testing.T) {
	t.Parallel()

	for _, permission := range slices.Concat(permissionsFor(nil), newPartDatabasesRole(Names{namespace: "ocel", project: "acme-prod"}).permissions) {
		if strings.HasPrefix(permission, "datastore.indexes.") {
			t.Errorf("a bootstrap or a deploy is granted %s, want datastore.schemas.*: Google's role reference lists only the schemas names", permission)
		}
	}
}

func addingTasks(t *testing.T) (*iamServer, bootstrap) {
	t.Helper()
	server := grantedIAM()
	return server, bootstrap{clients: server.open(t), records: fake.NewKeyValues()}
}

func TestTheFirstTopicOrTaskAddsTheTasksPartItRecordsAndMakesNoQueue(t *testing.T) {
	t.Parallel()
	server, b := addingTasks(t)
	ctx := context.Background()
	tier := environment.TierProduction

	if err := b.ensurePart(ctx, tier, "shop", b.tasksPart(tier)); err != nil {
		t.Fatalf("ensurePart(tasks) = %v", err)
	}
	if len(server.queuesMade) != 0 {
		t.Errorf("adding the tasks part made queues %v, want none: the base makes the delay queue", server.queuesMade)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(ctx, b.records, tier, tasksPart)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" {
		t.Errorf("the part record = %+v, %t, %v, want it ready and added by shop", recorded, present, err)
	}

}

func TestADryDeployDeclaringATaskPlansTheTasksPartAndMakesNothing(t *testing.T) {
	var planned []provider.ChangeGroup
	pre := declaringATask(naming.InfraStack("prod"))
	pre.Dry = true
	pre.AddToPlan = func(groups ...provider.ChangeGroup) { planned = append(planned, groups...) }

	if err := failingOnAnyCall(t).PreflightDeploy(context.Background(), pre); err != nil {
		t.Fatalf("PreflightDeploy() = %v", err)
	}
	if len(planned) != 1 || planned[0].Part != tasksPart || planned[0].Action != provider.ActionCreate {
		t.Errorf("a dry deploy declaring a task planned %+v, want the %s part created", planned, tasksPart)
	}
}

func TestRemovingABootstrapPurgesItsDelayQueueAndKeepsIt(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	b := bootstrap{clients: server.open(t)}
	ctx := context.Background()
	tier := environment.TierProduction
	read := survey{Tier: tier, Names: b.clients.Names, Emulated: true}
	queue := item{Kind: KindQueue, Name: b.clients.DelayQueue(tier)}
	read.present = map[string]bool{queue.ID(): true}

	if taking := removing(read, queue); taking.action != provider.ActionUpdate || taking.reason != reasonQueuePurged {
		t.Errorf("removing the delay queue plans %s (%s), want it purged and kept", taking.action, taking.reason)
	}
	if _, err := b.take(ctx, read, queue); err != nil {
		t.Fatalf("take(queue) = %v", err)
	}
	if !slices.Equal(server.queuesPurged, []string{b.delayQueuePath(tier)}) {
		t.Errorf("removing the bootstrap purged %v, want its delay queue: Cloud Tasks holds a deleted queue's name for days", server.queuesPurged)
	}
}
