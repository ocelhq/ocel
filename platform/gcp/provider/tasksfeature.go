package gcp

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"google.golang.org/api/cloudresourcemanager/v1"
	firestoreadmin "google.golang.org/api/firestore/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const (
	tasksFeature = "tasks"

	pubSubAgentDomain     = "@gcp-sa-pubsub.iam.gserviceaccount.com"
	cloudTasksAgentDomain = "@gcp-sa-cloudtasks.iam.gserviceaccount.com"

	taskRecordsRole   = "roles/datastore.user"
	tokenCreatorRole  = "roles/iam.serviceAccountTokenCreator"
	queueEnqueuerRole = "roles/cloudtasks.enqueuer"
	queueDeleterRole  = "roles/cloudtasks.taskDeleter"

	runsCollectionGroup    = "runs"
	recordsCollectionGroup = "records"
	runPurgeField          = "purgeAt"
	recordExpiryField      = "expiresAt"

	ascending        = "ASCENDING"
	descending       = "DESCENDING"
	contains         = "CONTAINS"
	collectionScoped = "COLLECTION"
)

var TasksAPIs = []string{"pubsub.googleapis.com", "cloudtasks.googleapis.com"}

var tasksPermissions = []string{
	"cloudtasks.queues.create",
	"cloudtasks.queues.get",
	"cloudtasks.queues.purge",
	"cloudtasks.queues.getIamPolicy",
	"cloudtasks.queues.setIamPolicy",
	"datastore.indexes.create",
	"datastore.indexes.get",
	"resourcemanager.projects.get",
}

var tasksRoles = []string{"roles/cloudtasks.admin"}

var queueRoles = []string{queueEnqueuerRole, queueDeleterRole}

func tasksSummary() string {
	return "a Firestore database of the tier's own that its runs are kept in, a Cloud Tasks queue a delayed message waits in, " +
		"and the account Pub/Sub signs each push to a worker as: what topics, tasks and workers run on, with no recurring cost of its own"
}

func taskDatabaseCondition(c *clients, tier environment.Tier) *cloudresourcemanager.Expr {
	return &cloudresourcemanager.Expr{
		Title:      "ocel " + string(c.Namespace()) + " " + string(tier) + " task database",
		Expression: fmt.Sprintf("resource.name == %q", databasePath(c, c.TaskDatabase(tier))),
	}
}

func (b bootstrap) raiseTasks(ctx context.Context, tier environment.Tier, progress progress.Log) error {
	if !b.clients.emulated() {
		if err := b.makeDatabase(ctx, survey{Project: b.clients.project, Region: b.clients.region}, b.clients.TaskDatabase(tier)); err != nil {
			return err
		}
		if err := b.ensureRunIndexes(ctx, tier); err != nil {
			return err
		}
	}
	if err := b.ensureDelayQueue(ctx, tier); err != nil {
		return err
	}
	if err := b.makeAccount(ctx, survey{Tier: tier}, b.clients.PushAccount(tier)); err != nil {
		return err
	}
	if err := b.grantTaskWrites(ctx, tier); err != nil {
		return err
	}
	ensureProgress(progress).Debug("The " + string(tier) + " tier keeps its runs in " + b.clients.TaskDatabase(tier) + " and delays messages in " + b.clients.DelayQueue(tier))
	return nil
}

func (b bootstrap) tasksFree(ctx context.Context, tier environment.Tier, features []string) error {
	if !slices.Contains(features, tasksFeature) {
		return nil
	}
	projects, err := b.records.List(ctx, stackrecords.ProjectsPartition(tier))
	if err != nil {
		return fmt.Errorf("read the projects deployed on tier %s: %w", tier, err)
	}
	var held []string
	for _, project := range projects {
		if len(project.Key.Path) != 1 {
			continue
		}
		slug := project.Key.Path[0]
		stacks, err := stackrecords.List(ctx, b.records, tier, slug)
		if err != nil {
			return err
		}
		for _, stack := range stacks {
			for _, binding := range stack.Bindings {
				if binding.Type != provider.BindingTopic && binding.Type != provider.BindingTask {
					continue
				}
				named := fmt.Sprintf("%s %s of project %s environment %s", binding.Type, cmp.Or(binding.Properties[topicDeclaredProperty], binding.Name), slug, stack.Name.Env)
				if !slices.Contains(held, named) {
					held = append(held, named)
				}
			}
		}
	}
	if len(held) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"tier %s still runs %s, and taking feature %s down deletes the task database their runs are kept in and the account Pub/Sub pushes to their workers as.\n"+
			"Remove those topics and tasks from their projects and deploy them, or destroy their environments, then remove feature %s again",
		tier, strings.Join(held, ", "), tasksFeature, tasksFeature)
}

func (b bootstrap) tearTasks(ctx context.Context, tier environment.Tier) error {
	steps := []func() error{
		func() error { return b.forgetTaskWrites(ctx, tier) },
		func() error { return b.takeAccount(ctx, tier, b.clients.PushAccount(tier)) },
		func() error { return b.purgeDelayQueue(ctx, tier) },
	}
	if !b.clients.emulated() {
		steps = append(steps, func() error { return b.takeDatabase(ctx, b.clients.TaskDatabase(tier)) })
	}
	return everyStep(steps...)
}

func (b bootstrap) tasksInstalled(ctx context.Context, tier environment.Tier) (bool, error) {
	if !b.clients.emulated() {
		database, err := b.databasePresence(ctx, b.clients.TaskDatabase(tier))
		if err != nil || !database.present {
			return false, err
		}
	}
	queue, err := b.readDelayQueue(ctx, tier)
	if err != nil || queue == nil {
		return false, err
	}
	account, err := b.accountPresence(ctx, tier, b.clients.PushAccount(tier))
	if err != nil {
		return false, err
	}
	return account.present && account.mends == "", nil
}

func (b bootstrap) pushPurpose(tier environment.Tier) accountPurpose {
	return accountPurpose{
		displayName: "ocel " + string(tier) + " pushes",
		description: "the identity Pub/Sub signs each push to a worker of the " + string(tier) + " tier as, and the one alone that may invoke a worker",
		ungranted:   "it exists, and Pub/Sub may not sign a push as it, so no worker would ever be reached",
		grant:       b.grantPushSigning,
		forget:      b.forgetPushSigning,
		granted:     b.pushSigningGranted,
	}
}

func (b bootstrap) grantPushSigning(ctx context.Context, tier environment.Tier) error {
	agent, err := b.clients.ReadServiceAgent(ctx, pubSubAgentDomain)
	if err != nil {
		return err
	}
	return b.clients.bindAccountRole(ctx, b.clients.PushAccount(tier), tokenCreatorRole, agent, true)
}

func (b bootstrap) forgetPushSigning(ctx context.Context, tier environment.Tier) error {
	agent, err := b.clients.ReadServiceAgent(ctx, pubSubAgentDomain)
	if err != nil {
		return err
	}
	return b.clients.bindAccountRole(ctx, b.clients.PushAccount(tier), tokenCreatorRole, agent, false)
}

func (b bootstrap) pushSigningGranted(ctx context.Context, tier environment.Tier) (bool, error) {
	agent, err := b.clients.ReadServiceAgent(ctx, pubSubAgentDomain)
	if err != nil {
		return false, err
	}
	return b.clients.accountRoleGranted(ctx, b.clients.PushAccount(tier), tokenCreatorRole, agent)
}

func (b bootstrap) grantTaskWrites(ctx context.Context, tier environment.Tier) error {
	return b.bindTaskWrites(ctx, tier, true)
}

func (b bootstrap) forgetTaskWrites(ctx context.Context, tier environment.Tier) error {
	return b.bindTaskWrites(ctx, tier, false)
}

func (b bootstrap) bindTaskWrites(ctx context.Context, tier environment.Tier, granting bool) error {
	member := workloadMember(b.clients, tier)
	if err := b.clients.bindProjectRole(ctx, member, taskRecordsRole, taskDatabaseCondition(b.clients, tier), granting); err != nil {
		return fmt.Errorf("let %s read and write the runs in %s: %w", member, b.clients.TaskDatabase(tier), err)
	}
	agent, err := b.clients.ReadServiceAgent(ctx, cloudTasksAgentDomain)
	if err != nil {
		return err
	}
	for _, actor := range []string{member, agent} {
		if err := b.clients.bindAccountRole(ctx, b.clients.WorkloadAccount(tier), runAsRole, actor, granting); err != nil {
			return err
		}
	}
	return nil
}

func (b bootstrap) delayQueuePath(tier environment.Tier) string {
	return b.clients.DelayQueuePath(b.clients.region, tier)
}

func (b bootstrap) readDelayQueue(ctx context.Context, tier environment.Tier) (*cloudtaskspb.Queue, error) {
	client, err := b.clients.Workload().CloudTasks()
	if err != nil {
		return nil, err
	}
	queue, err := dialled(ctx, func() (*cloudtaskspb.Queue, error) {
		return client.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: b.delayQueuePath(tier)})
	})
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s delay queue: %w", b.clients.DelayQueue(tier), err)
	}
	return queue, nil
}

func (b bootstrap) ensureDelayQueue(ctx context.Context, tier environment.Tier) error {
	client, err := b.clients.Workload().CloudTasks()
	if err != nil {
		return err
	}
	path := b.delayQueuePath(tier)
	if err := (topics.Delays{Queue: path}).Ensure(ctx, b.clients.Workload()); err != nil {
		return err
	}
	policy, err := dialled(ctx, func() (*iampb.Policy, error) {
		return client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: path})
	})
	if err != nil {
		return fmt.Errorf("read who may delay messages on %s: %w", b.clients.DelayQueue(tier), err)
	}
	bindings, changed := boundKeyRoles(policy.GetBindings(), workloadMember(b.clients, tier), queueRoles, queueRoles)
	if !changed {
		return nil
	}
	policy.Bindings = bindings
	if _, err := dialled(ctx, func() (*iampb.Policy, error) {
		return client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: path, Policy: policy})
	}); err != nil {
		return fmt.Errorf("let the %s apps delay messages on %s: %w", tier, b.clients.DelayQueue(tier), err)
	}
	return nil
}

func (b bootstrap) purgeDelayQueue(ctx context.Context, tier environment.Tier) error {
	client, err := b.clients.Workload().CloudTasks()
	if err != nil {
		return err
	}
	_, err = dialled(ctx, func() (*cloudtaskspb.Queue, error) {
		return client.PurgeQueue(ctx, &cloudtaskspb.PurgeQueueRequest{Name: b.delayQueuePath(tier)})
	})
	if err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("purge the %s delay queue: %w", b.clients.DelayQueue(tier), err)
	}
	return nil
}

func runIndexes() []*firestoreadmin.GoogleFirestoreAdminV1Index {
	field := func(path, order string) *firestoreadmin.GoogleFirestoreAdminV1IndexField {
		if order == contains {
			return &firestoreadmin.GoogleFirestoreAdminV1IndexField{FieldPath: path, ArrayConfig: contains}
		}
		return &firestoreadmin.GoogleFirestoreAdminV1IndexField{FieldPath: path, Order: order}
	}
	newest := field("createdAt", descending)
	filters := [][]*firestoreadmin.GoogleFirestoreAdminV1IndexField{
		{field("topic", ascending)},
		{field("status", ascending)},
		{field("tags", contains)},
		{field("topic", ascending), field("status", ascending)},
		{field("topic", ascending), field("tags", contains)},
		{field("status", ascending), field("tags", contains)},
		{field("topic", ascending), field("status", ascending), field("tags", contains)},
	}
	indexes := make([]*firestoreadmin.GoogleFirestoreAdminV1Index, 0, len(filters)+1)
	for _, filter := range filters {
		indexes = append(indexes, &firestoreadmin.GoogleFirestoreAdminV1Index{QueryScope: collectionScoped, Fields: append(slices.Clone(filter), newest)})
	}
	return append(indexes, &firestoreadmin.GoogleFirestoreAdminV1Index{QueryScope: collectionScoped, Fields: []*firestoreadmin.GoogleFirestoreAdminV1IndexField{
		field("topic", ascending), field("consumer", ascending), field("status", ascending), field("finishedAt", ascending),
	}})
}

func (b bootstrap) ensureRunIndexes(ctx context.Context, tier environment.Tier) error {
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	database := databasePath(b.clients, b.clients.TaskDatabase(tier))
	for group, field := range map[string]string{runsCollectionGroup: runPurgeField, recordsCollectionGroup: recordExpiryField} {
		name := database + "/collectionGroups/" + group + "/fields/" + field
		operation, err := attempted(ctx, service.Projects.Databases.CollectionGroups.Fields.Patch(name,
			&firestoreadmin.GoogleFirestoreAdminV1Field{TtlConfig: &firestoreadmin.GoogleFirestoreAdminV1TtlConfig{}}).
			UpdateMask("ttlConfig").Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("let Firestore delete %s once %s passes: %w", group, field, err)
		}
		if err := b.awaited(ctx, fmt.Sprintf("setting the time to live of %s on %s", group, field), operation); err != nil {
			return err
		}
	}
	parent := database + "/collectionGroups/" + runsCollectionGroup
	for _, index := range runIndexes() {
		operation, err := attempted(ctx, service.Projects.Databases.CollectionGroups.Indexes.Create(parent, index).Context(ctx).Do)
		if taken(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("index the runs of %s: %w", b.clients.TaskDatabase(tier), err)
		}
		if err := b.awaited(ctx, "building an index on the runs of "+b.clients.TaskDatabase(tier), operation); err != nil {
			return err
		}
	}
	return nil
}
