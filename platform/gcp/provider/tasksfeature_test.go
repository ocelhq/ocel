package gcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/iam/v1"

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

	b := bootstrap{clients: grantedIAM().open(t), records: recordingTopics(t, environment.TierPreview,
		provider.Binding{Type: provider.BindingTopic, Name: "topic--orders", Properties: map[string]string{topicDeclaredProperty: "orders"}},
	)}
	if err := b.tasksFree(context.Background(), environment.TierProduction, []string{tasksFeature}); err != nil {
		t.Errorf("tasksFree() with a topic only on another tier = %v, want the feature free", err)
	}
}

const (
	cloudTasksAgent = "serviceAccount:service-123456789@gcp-sa-cloudtasks.iam.gserviceaccount.com"
	pubSubAgent     = "serviceAccount:service-123456789@gcp-sa-pubsub.iam.gserviceaccount.com"
)

func accountPolicyPath(email string) string {
	return "/v1/projects/acme-prod/serviceAccounts/" + email
}

func runAsPolicy(members ...string) *iam.Policy {
	return &iam.Policy{Etag: "BwXhoLA=", Bindings: []*iam.Binding{{Role: runAsRole, Members: members}}}
}

func TestTheTasksFeatureGivesTheTierAnAccountCloudTasksSignsNextRefreshesAs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server := grantedIAM()
	server.accountPolicies = map[string]*iam.Policy{}
	b := bootstrap{clients: server.open(t)}
	tier := environment.TierProduction

	if err := b.raiseTasks(ctx, tier, nil); err != nil {
		t.Fatalf("raiseTasks() = %v", err)
	}

	policy := server.accountPolicies[accountPolicyPath(b.clients.RefreshAccountEmail(tier))]
	if policy == nil {
		t.Fatalf("raiseTasks() left no policy on the refresh account, want Cloud Tasks granted to sign as it")
	}
	var members []string
	for _, binding := range policy.Bindings {
		if binding.Role != runAsRole {
			t.Errorf("the refresh account binds %s, want no role but %s", binding.Role, runAsRole)
			continue
		}
		members = append(members, binding.Members...)
	}
	slices.Sort(members)
	if want := []string{cloudTasksAgent, "user:emulator"}; !slices.Equal(members, want) {
		t.Errorf("the refresh account may be run as by %v, want %v", members, want)
	}
	if !slices.ContainsFunc(server.created, func(r *iam.CreateServiceAccountRequest) bool {
		return r.AccountId == b.clients.RefreshAccount(tier) && r.ServiceAccount.DisplayName == "ocel production refreshes"
	}) {
		t.Errorf("raiseTasks() created %v, want the refresh account named \"ocel production refreshes\"", server.created)
	}
	found, err := b.accountPresence(ctx, tier, b.clients.RefreshAccount(tier))
	if err != nil || found != (presence{present: true}) {
		t.Errorf("accountPresence() = %+v, %v, want it present with nothing to mend", found, err)
	}
}

func TestARefreshAccountCloudTasksMayNotSignAsIsSurveyedAsMendable(t *testing.T) {
	t.Parallel()
	b := bootstrap{clients: grantedIAM().open(t)}
	tier := environment.TierProduction

	found, err := b.accountPresence(context.Background(), tier, b.clients.RefreshAccount(tier))
	purpose, purposeErr := b.purposeOf(tier, b.clients.RefreshAccount(tier))
	if purposeErr != nil {
		t.Fatalf("purposeOf() = %v", purposeErr)
	}
	if want := purpose.ungranted; err != nil || !found.present || found.mends != want {
		t.Errorf("accountPresence() = %+v, %v, want it present and mended with %q", found, err, want)
	}
}

func TestATierWithoutItsRefreshAccountHasNotInstalledTasks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server := grantedIAM()
	server.accounts = map[string]bool{}
	server.accountPolicies = map[string]*iam.Policy{}
	b := bootstrap{clients: server.open(t)}
	tier := environment.TierProduction
	server.accounts[b.clients.PushAccount(tier)] = true
	server.accountPolicies[accountPolicyPath(b.clients.PushAccountEmail(tier))] = &iam.Policy{Etag: "BwXhoLA=", Bindings: []*iam.Binding{
		{Role: runAsRole, Members: []string{"user:emulator"}},
		{Role: tokenCreatorRole, Members: []string{pubSubAgent}},
	}}

	if installed, err := b.tasksInstalled(ctx, tier); err != nil || installed {
		t.Fatalf("tasksInstalled() = %v, %v without the refresh account, want false", installed, err)
	}

	server.accounts[b.clients.RefreshAccount(tier)] = true
	server.accountPolicies[accountPolicyPath(b.clients.RefreshAccountEmail(tier))] = runAsPolicy(cloudTasksAgent, "user:emulator")
	if installed, err := b.tasksInstalled(ctx, tier); err != nil || !installed {
		t.Errorf("tasksInstalled() = %v, %v with the refresh account, want true", installed, err)
	}
}

func TestTakingTasksDownDeletesTheRefreshAccountAndCloudTasksMaySignAsItNoMore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server := grantedIAM()
	server.accountPolicies = map[string]*iam.Policy{}
	b := bootstrap{clients: server.open(t)}
	tier := environment.TierProduction
	if err := b.raiseTasks(ctx, tier, nil); err != nil {
		t.Fatal(err)
	}

	if err := b.tearTasks(ctx, tier); err != nil {
		t.Fatalf("tearTasks() = %v", err)
	}

	path := accountPolicyPath(b.clients.RefreshAccountEmail(tier))
	if !slices.Contains(server.deletedAccounts, path) {
		t.Errorf("tearTasks() deleted %v, want %s", server.deletedAccounts, path)
	}
	for _, binding := range server.accountPolicies[path].Bindings {
		if slices.Contains(binding.Members, cloudTasksAgent) {
			t.Errorf("the refresh account still lets Cloud Tasks sign as it in %s: %v", binding.Role, binding.Members)
		}
	}
}

func TestRemovingTasksIsRefusedWhileANextAppRefreshesThroughThem(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	server.accountPolicies = map[string]*iam.Policy{}
	b := bootstrap{clients: server.open(t), records: recordingTopics(t, environment.TierProduction)}
	tier := environment.TierProduction
	app := b.clients.AppAccountEmail(tier, "shop", "web")
	server.accountDescriptions = map[string]string{app: appAccountDescription(tier, "shop", "web")}
	server.accountPolicies[accountPolicyPath(b.clients.RefreshAccountEmail(tier))] = runAsPolicy(
		"user:emulator", cloudTasksAgent, "serviceAccount:"+app,
		"deleted:serviceAccount:ocel-0123456789@acme-prod.iam.gserviceaccount.com?uid=1")

	err := b.tasksFree(context.Background(), tier, []string{tasksFeature})

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("tasksFree() = %v, want an invalid refusal", err)
	}
	for _, want := range []string{"web of project shop", "last environment", "container compute"} {
		if !strings.Contains(refused.Message, want) {
			t.Errorf("the refusal is %q, want it to say %q", refused.Message, want)
		}
	}
	if strings.Contains(refused.Message, app) {
		t.Errorf("the refusal is %q, want the app named rather than its account %s", refused.Message, app)
	}
	for _, leaked := range []string{"gcp-sa-cloudtasks", "emulator", "uid=1"} {
		if strings.Contains(refused.Message, leaked) {
			t.Errorf("the refusal names %q, want only the Next apps that run as accounts of ocel's", leaked)
		}
	}
}

func TestRemovingTasksFromATierBootstrappedBeforeItsRefreshAccountIsNotRefused(t *testing.T) {
	t.Parallel()
	server := grantedIAM()
	server.tasksAbsent = true
	b := bootstrap{clients: server.open(t), records: recordingTopics(t, environment.TierProduction)}

	if err := b.tasksFree(context.Background(), environment.TierProduction, []string{tasksFeature}); err != nil {
		t.Errorf("tasksFree() with no refresh account = %v, want the feature free", err)
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
