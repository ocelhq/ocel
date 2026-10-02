package gcp

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

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
	for _, permission := range []string{"cloudtasks.queues.create", "cloudtasks.queues.setIamPolicy", "datastore.indexes.create", "resourcemanager.projects.get"} {
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
