package task

import (
	"context"
	"slices"

	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
)

const (
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

func RunLayout() variables.Layout {
	layout := variables.Layout{Expiring: []variables.LayoutField{
		{Group: runsCollectionGroup, Path: runPurgeField},
		{Group: recordsCollectionGroup, Path: recordExpiryField},
	}}
	for _, index := range RunIndexes() {
		layout.Indexes = append(layout.Indexes, variables.LayoutIndex{Group: runsCollectionGroup, Index: index})
	}
	return layout
}

func DelayQueueShape(names names.Names, tier environment.Tier, region string) pricing.Shaped {
	queue := bootstrap.Resource{Kind: bootstrap.KindQueue, Name: names.DelayQueue(tier)}
	shaped, _ := queue.Shape(region)
	return shaped
}
