package task

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
)

const (
	PartName = "tasks"
	Summary  = "a Firestore database of the tier's own that the runs of its topics, tasks and workers are kept in, with no recurring cost of its own"

	pubSubAgentDomain     = "@gcp-sa-pubsub.iam.gserviceaccount.com"
	CloudTasksAgentDomain = "@gcp-sa-cloudtasks.iam.gserviceaccount.com"

	QueueEnqueuerRole = "roles/cloudtasks.enqueuer"

	runsCollectionGroup    = "runs"
	recordsCollectionGroup = "records"
	runPurgeField          = "purgeAt"
	recordExpiryField      = "expiresAt"

	Ascending        = "ASCENDING"
	descending       = "DESCENDING"
	contains         = "CONTAINS"
	CollectionScoped = "COLLECTION"
)

var QueueRoles = []string{QueueEnqueuerRole}

func DatabaseCondition(c *gcloud.Clients, tier environment.Tier) *policy.Condition {
	return &policy.Condition{
		Title:      "ocel " + string(c.Namespace()) + " " + string(tier) + " task database",
		Expression: fmt.Sprintf("resource.name == %q", bootstrap.DatabasePath(c, c.TaskDatabase(tier))),
	}
}

func NewPart(b bootstrap.Engine, tier environment.Tier) bootstrap.Part {
	var resources []provider.PartResource
	if !b.Clients.Emulated() {
		resources = append(resources, provider.PartResource{Kind: string(bootstrap.KindDatabase), ID: b.Clients.TaskDatabase(tier)})
	}
	return bootstrap.Part{
		Name:      PartName,
		Summary:   Summary,
		Resources: resources,
		Shape:     Shape(b.Clients.Names, tier, b.Clients.Region),
		Make:      func(ctx context.Context) error { return makeTasks(ctx, b, tier) },
		Users:     func(ctx context.Context) ([]string, error) { return ListUsers(ctx, b, tier) },
		RefuseInUse: func(ctx context.Context, removal bootstrap.PartRemoval) error {
			return RefuseInUse(ctx, b, tier, removal)
		},
		Remove: func(ctx context.Context) error { return removeTasks(ctx, b, tier) },
	}
}

func Shape(names names.Names, tier environment.Tier, region string) []pricing.Shaped {
	return []pricing.Shaped{
		{Name: names.TaskDatabase(tier), Type: cost.FirestoreDatabaseType, Properties: map[string]any{"location_id": region, "type": bootstrap.NativeFirestore}},
	}
}

func makeTasks(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	if b.Clients.Emulated() {
		return nil
	}
	if err := b.MakeDatabase(ctx, bootstrap.Survey{Project: b.Clients.Project(), Region: b.Clients.Region}, b.Clients.TaskDatabase(tier)); err != nil {
		return err
	}
	return ensureRunIndexes(ctx, b, tier)
}

type taskHolder struct {
	named   string
	project string
}

func listTaskHolders(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]taskHolder, error) {
	projects, err := b.Records.List(ctx, stackrecords.ProjectsPartition(tier))
	if err != nil {
		return nil, fmt.Errorf("read the projects deployed on tier %s: %w", tier, err)
	}
	var held []taskHolder
	for _, project := range projects {
		if len(project.Key.Path) != 1 {
			continue
		}
		slug := project.Key.Path[0]
		stacks, err := stackrecords.List(ctx, b.Records, tier, slug)
		if err != nil {
			return nil, err
		}
		for _, stack := range stacks {
			for _, binding := range stack.Bindings {
				if binding.Type != provider.BindingTopic && binding.Type != provider.BindingTask {
					continue
				}
				named := fmt.Sprintf("%s %s of project %s environment %s", binding.Type, cmp.Or(binding.Properties[stackrecords.PropertyDeclared], binding.Name), slug, stack.Name.Env)
				if !slices.ContainsFunc(held, func(each taskHolder) bool { return each.named == named }) {
					held = append(held, taskHolder{named: named, project: slug})
				}
			}
		}
	}
	return held, nil
}

func ListUsers(ctx context.Context, b bootstrap.Engine, tier environment.Tier) ([]string, error) {
	held, err := listTaskHolders(ctx, b, tier)
	if err != nil {
		return nil, err
	}
	users := []string{}
	for _, each := range held {
		users = append(users, each.project)
	}
	slices.Sort(users)
	return slices.Compact(users), nil
}

func RefuseInUse(ctx context.Context, b bootstrap.Engine, tier environment.Tier, removal bootstrap.PartRemoval) error {
	if removal.Force {
		return nil
	}
	held, err := listTaskHolders(ctx, b, tier)
	if err != nil || len(held) == 0 {
		return err
	}
	named := make([]string, 0, len(held))
	for _, each := range held {
		named = append(named, each.named)
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"tier %s still runs %s, and removing the %s part deletes the task database their runs are kept in.\n"+
			"Remove those topics and tasks from their projects and deploy them, or destroy their environments, then run `%s` again",
		tier, strings.Join(named, ", "), PartName, removal.Retry)
}

func removeTasks(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	if b.Clients.Emulated() {
		return nil
	}
	return b.TakeDatabase(ctx, b.Clients.TaskDatabase(tier))
}

func PushPurpose(b bootstrap.Engine, tier environment.Tier) bootstrap.AccountPurpose {
	return bootstrap.AccountPurpose{
		DisplayName: "ocel " + string(tier) + " pushes",
		Description: "the identity Pub/Sub signs each push to a worker of the " + string(tier) + " tier as, and the one alone that may invoke a worker",
		Ungranted:   "it exists, and Pub/Sub may not sign a push as it, so no worker would ever be reached",
		Grant:       grantPushSigning,
		Forget:      forgetPushSigning,
		Granted:     pushSigningGranted,
	}
}

func grantPushSigning(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	agent, err := b.Clients.ReadServiceAgent(ctx, pubSubAgentDomain)
	if err != nil {
		return err
	}
	return gcloud.BindAccountRole(ctx, b.Clients, b.Clients.PushAccount(tier), grants.TokenCreatorRole, agent, true)
}

func forgetPushSigning(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	agent, err := b.Clients.ReadServiceAgent(ctx, pubSubAgentDomain)
	if err != nil {
		return err
	}
	return gcloud.BindAccountRole(ctx, b.Clients, b.Clients.PushAccount(tier), grants.TokenCreatorRole, agent, false)
}

func pushSigningGranted(ctx context.Context, b bootstrap.Engine, tier environment.Tier) (bool, error) {
	agent, err := b.Clients.ReadServiceAgent(ctx, pubSubAgentDomain)
	if err != nil {
		return false, err
	}
	return gcloud.AccountRoleGranted(ctx, b.Clients, b.Clients.PushAccount(tier), grants.TokenCreatorRole, agent)
}

func BindQueueRoles(ctx context.Context, c *gcloud.Clients, tier environment.Tier, member string, roles, wanted []string) error {
	changes := make([]policy.Change, 0, len(roles))
	for _, role := range roles {
		changes = append(changes, gcloud.GrantedOrRevoked(role, member, nil, slices.Contains(wanted, role)))
	}
	_, err := policy.Edit(ctx, gcloud.QueuePolicy(c, c.DelayQueuePath(c.Region, tier), "the "+c.DelayQueue(tier)+" delay queue"), policy.Each(changes...))
	return err
}

func RunIndexes() []*firestoreadmin.GoogleFirestoreAdminV1Index {
	field := func(path, order string) *firestoreadmin.GoogleFirestoreAdminV1IndexField {
		if order == contains {
			return &firestoreadmin.GoogleFirestoreAdminV1IndexField{FieldPath: path, ArrayConfig: contains}
		}
		return &firestoreadmin.GoogleFirestoreAdminV1IndexField{FieldPath: path, Order: order}
	}
	var indexes []*firestoreadmin.GoogleFirestoreAdminV1Index
	for _, consumer := range [][]*firestoreadmin.GoogleFirestoreAdminV1IndexField{nil, {field("consumer", Ascending)}} {
		for _, status := range [][]*firestoreadmin.GoogleFirestoreAdminV1IndexField{nil, {field("status", Ascending)}} {
			for _, tags := range [][]*firestoreadmin.GoogleFirestoreAdminV1IndexField{nil, {field("tags", contains)}} {
				fields := slices.Concat([]*firestoreadmin.GoogleFirestoreAdminV1IndexField{field("topic", Ascending)}, consumer, status, tags)
				indexes = append(indexes, &firestoreadmin.GoogleFirestoreAdminV1Index{QueryScope: CollectionScoped, Fields: append(fields, field("createdAt", descending))})
			}
		}
	}
	return indexes
}

func ensureRunIndexes(ctx context.Context, b bootstrap.Engine, tier environment.Tier) error {
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	database := bootstrap.DatabasePath(b.Clients, b.Clients.TaskDatabase(tier))
	for group, field := range map[string]string{runsCollectionGroup: runPurgeField, recordsCollectionGroup: recordExpiryField} {
		name := database + "/collectionGroups/" + group + "/fields/" + field
		operation, err := googleretry.Attempted(ctx, service.Projects.Databases.CollectionGroups.Fields.Patch(name,
			&firestoreadmin.GoogleFirestoreAdminV1Field{TtlConfig: &firestoreadmin.GoogleFirestoreAdminV1TtlConfig{}}).
			UpdateMask("ttlConfig").Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("let Firestore delete %s once %s passes: %w", group, field, err)
		}
		if err := b.Awaited(ctx, fmt.Sprintf("setting the time to live of %s on %s", group, field), operation); err != nil {
			return err
		}
	}
	parent := database + "/collectionGroups/" + runsCollectionGroup
	for _, index := range RunIndexes() {
		operation, err := googleretry.Attempted(ctx, service.Projects.Databases.CollectionGroups.Indexes.Create(parent, index).Context(ctx).Do)
		if gcloud.Taken(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("index the runs of %s: %w", b.Clients.TaskDatabase(tier), err)
		}
		if err := b.Awaited(ctx, "building an index on the runs of "+b.Clients.TaskDatabase(tier), operation); err != nil {
			return err
		}
	}
	return nil
}

var Part = component.Part{Name: PartName, Shape: costShape}

func costShape(names names.Names, tier environment.Tier, region string) []pricing.Shaped {
	queue := bootstrap.Resource{Kind: bootstrap.KindQueue, Name: names.DelayQueue(tier)}
	shaped, _ := queue.Shape(region)
	return append(Shape(names, tier, region), shaped)
}
