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
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
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

func TestDroppingTasksWhileAnEnvironmentDeclaresATopicIsRefusedNamingIt(t *testing.T) {
	t.Parallel()

	b := bootstrap{records: recordingTopics(t, environment.TierProduction,
		provider.Binding{Type: provider.BindingBucket, Name: "uploads"},
		provider.Binding{Type: provider.BindingTask, Name: "task--resize", Properties: map[string]string{topicDeclaredProperty: "resize"}},
	)}

	drop := provider.BootstrapRequest{Tier: environment.TierProduction, Remove: []string{tasksFeature}}
	err := b.dropFeatures(context.Background(), surveyed(tasksFeature), drop, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "task resize of project shop environment prod") {
		t.Fatalf("dropFeatures() while a task is declared = %v, want a refusal naming the task, its project and environment", err)
	}
	if strings.Contains(refused.Message, "uploads") {
		t.Errorf("the refusal names %q, want only what runs on the feature", refused.Message)
	}
}

func TestDroppingTasksIgnoresATopicOnAnotherTier(t *testing.T) {
	t.Parallel()

	b := bootstrap{records: recordingTopics(t, environment.TierPreview,
		provider.Binding{Type: provider.BindingTopic, Name: "topic--orders", Properties: map[string]string{topicDeclaredProperty: "orders"}},
	)}
	if err := b.tasksFree(context.Background(), environment.TierProduction, []string{tasksFeature}); err != nil {
		t.Errorf("tasksFree() with a topic only on another tier = %v, want the feature free", err)
	}
}

func TestTopicsAndTasksAreAFeatureNoEdgePullsIn(t *testing.T) {
	t.Parallel()

	catalogue := bootstrap{}.Catalogue()
	if !slices.ContainsFunc(catalogue, func(f provider.Feature) bool { return f.Name == tasksFeature && len(f.Edges) == 0 }) {
		t.Fatalf("Catalogue() = %v, want the %q feature topics, tasks and workers run on, pulled in by no edge", catalogue, tasksFeature)
	}
	for _, kind := range []edge.Kind{edge.None, alb.Kind} {
		required, err := bootstrapplan.RequiredFeatures(catalogue, nil, kind)
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(required, tasksFeature) {
			t.Errorf("a bootstrap fronted by %q requires %v, and a queue is raised only for a tier that asks for topics and tasks", kind, required)
		}
	}
}

func TestTopicsAndTasksCheckTheServicesAndPermissionsTheyAreRaisedWith(t *testing.T) {
	t.Parallel()

	for _, api := range []string{"pubsub.googleapis.com", "cloudtasks.googleapis.com", "firestore.googleapis.com", "cloudscheduler.googleapis.com"} {
		if !slices.Contains(apisFor([]string{tasksFeature}), api) {
			t.Errorf("a %q bootstrap checks %v, want %s among them", tasksFeature, apisFor([]string{tasksFeature}), api)
		}
	}
	if slices.Contains(apisFor(nil), "pubsub.googleapis.com") {
		t.Errorf("a bootstrap with no feature checks %v, and a project that runs no topic needs no Pub/Sub switched on", apisFor(nil))
	}
	for _, permission := range []string{"cloudtasks.queues.create", "cloudtasks.queues.setIamPolicy", "datastore.schemas.create", "resourcemanager.projects.get"} {
		if !slices.Contains(permissionsFor([]string{tasksFeature}), permission) {
			t.Errorf("a %q bootstrap checks %v, want %s among them", tasksFeature, permissionsFor([]string{tasksFeature}), permission)
		}
	}
	if !slices.Contains(rolesCovering([]string{tasksFeature}), "roles/cloudtasks.admin") {
		t.Errorf("a %q bootstrap names %v as covering it, want roles/cloudtasks.admin", tasksFeature, rolesCovering([]string{tasksFeature}))
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
	for _, features := range [][]string{nil, {tasksFeature}} {
		for permission, call := range needs {
			if !slices.Contains(permissionsFor(features), permission) {
				t.Errorf("a bootstrap with features %v checks %v, want %s among them: %s would fail mid-apply", features, permissionsFor(features), permission, call)
			}
		}
	}
}

func TestABootstrapChecksFirestoreIndexPermissionsByTheirCurrentNames(t *testing.T) {
	t.Parallel()

	for _, features := range [][]string{nil, {tasksFeature}} {
		for _, permission := range permissionsFor(features) {
			if strings.HasPrefix(permission, "datastore.indexes.") {
				t.Errorf("a bootstrap with features %v checks %s, want datastore.schemas.*: Google's role reference lists only the schemas names", features, permission)
			}
		}
	}
}
