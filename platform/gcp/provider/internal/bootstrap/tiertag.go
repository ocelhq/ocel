package bootstrap

import (
	"context"
	"fmt"
	"slices"

	crm "google.golang.org/api/cloudresourcemanager/v3"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const (
	ReasonTierTagKept     = "every namespace and tier of this project binds its values"
	reasonTierValueAbsent = "it lacks a tier's value"
	reasonUntagged        = "it carries no ocel-tier tag"

	tagKeyReadPermission   = "resourcemanager.tagKeys.get"
	tagValueReadPermission = "resourcemanager.tagValues.get"
)

var tierTagKind = KindOps{
	noun:      "tag key",
	terraform: cost.TagKeyType,
	survey: func(ctx context.Context, b Engine, _ Survey, _ Resource) (Presence, error) {
		return b.tierTagPresence(ctx)
	},
	Make: func(ctx context.Context, b Engine, _ Survey, _ Resource) error {
		return b.makeTierTag(ctx)
	},
	removal: func(_ Survey, target Resource) Removal { return kept(target, ReasonTierTagKept) },
}

func tierTagValues() []environment.Tier {
	return []environment.Tier{environment.TierProduction, environment.TierPreview}
}

func (b Engine) tierTagPresence(ctx context.Context) (Presence, error) {
	if !b.Clients.TierTagged() {
		return Presence{Present: true}, nil
	}
	key, err := b.readTierTagKey(ctx)
	if key == nil || err != nil {
		return Presence{}, err
	}
	missing, err := b.missingTierValues(ctx)
	if err != nil {
		return Presence{}, err
	}
	if len(missing) > 0 {
		return Presence{Present: true, Mends: reasonTierValueAbsent}, nil
	}
	return Presence{Present: true}, nil
}

func (b Engine) readTierTagKey(ctx context.Context) (*crm.TagKey, error) {
	service, err := b.Clients.GlobalTagService()
	if err != nil {
		return nil, err
	}
	key, err := googleretry.Attempted(ctx, service.TagKeys.GetNamespaced().Name(b.Clients.TierTagKeyPath()).Context(ctx).Do)
	if err == nil {
		return key, nil
	}
	return nil, b.refuseUnreadableTag(ctx, err, b.Clients.TierTagKeyPath(), tagKeyReadPermission)
}

func (b Engine) refuseUnreadableTag(ctx context.Context, err error, path, permission string) error {
	switch {
	case gcloud.Absent(err):
		return nil
	case !gcloud.Forbidden(err):
		return fmt.Errorf("read the %s tag: %w", path, err)
	}
	held, err := b.heldPermissions(ctx, []string{permission})
	if err != nil || slices.Contains(held, permission) {
		return err
	}
	return refusal.Refuse(refusal.CodeDenied,
		"this credential lacks %s on project %s, and Google answers a tag it may not read as it answers a tag that does not exist, so whether %s exists is unknown.\n"+
			"Granting roles/resourcemanager.tagAdmin, which a bootstrap credential holds, covers it",
		permission, b.Clients.Project(), path)
}

func (b Engine) missingTierValues(ctx context.Context) ([]environment.Tier, error) {
	service, err := b.Clients.GlobalTagService()
	if err != nil {
		return nil, err
	}
	var missing []environment.Tier
	for _, tier := range tierTagValues() {
		path := b.Clients.TierTagValuePath(tier)
		_, err := googleretry.Attempted(ctx, service.TagValues.GetNamespaced().Name(path).Context(ctx).Do)
		if err == nil {
			continue
		}
		if err := b.refuseUnreadableTag(ctx, err, path, tagValueReadPermission); err != nil {
			return nil, err
		}
		missing = append(missing, tier)
	}
	return missing, nil
}

func (b Engine) makeTierTag(ctx context.Context) error {
	if !b.Clients.TierTagged() {
		return nil
	}
	service, err := b.Clients.GlobalTagService()
	if err != nil {
		return err
	}
	key, err := b.readTierTagKey(ctx)
	if err != nil {
		return err
	}
	if key == nil {
		if key, err = b.createTierTagKey(ctx, service); err != nil {
			return err
		}
	}
	missing, err := b.missingTierValues(ctx)
	if err != nil {
		return err
	}
	for _, tier := range missing {
		path := b.Clients.TierTagValuePath(tier)
		started, err := googleretry.Attempted(ctx, service.TagValues.Create(&crm.TagValue{
			Parent:      key.Name,
			ShortName:   string(tier),
			Description: "carried by every resource ocel makes for the " + string(tier) + " tier",
		}).Context(ctx).Do)
		if gcloud.Taken(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create the %s tag value: %w", path, err)
		}
		if err := gcloud.TagOperationDone(ctx, service, "creating the "+path+" tag value", started); err != nil {
			return err
		}
	}
	return nil
}

func (b Engine) createTierTagKey(ctx context.Context, service *crm.Service) (*crm.TagKey, error) {
	started, err := googleretry.Attempted(ctx, service.TagKeys.Create(&crm.TagKey{
		Parent:      "projects/" + b.Clients.Project(),
		ShortName:   names.TierTagKey,
		Description: "the tier an ocel resource belongs to, which the tier's deploy credential is fenced by",
	}).Context(ctx).Do)
	if err != nil && !gcloud.Taken(err) {
		return nil, fmt.Errorf("create the %s tag key: %w", b.Clients.TierTagKeyPath(), err)
	}
	if err == nil {
		if err := gcloud.TagOperationDone(ctx, service, "creating the "+b.Clients.TierTagKeyPath()+" tag key", started); err != nil {
			return nil, err
		}
	}
	key, err := b.readTierTagKey(ctx)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, fmt.Errorf("the %s tag key was created and cannot be read back", b.Clients.TierTagKeyPath())
	}
	return key, nil
}

func (b Engine) tierTagged(ctx context.Context, read Survey, target Resource) (gcloud.TaggedResource, bool, error) {
	tagged := target.KindOps().tagged
	if tagged == nil || !b.Clients.TierTagged() {
		return gcloud.TaggedResource{}, false, nil
	}
	return tagged(ctx, b, read, target)
}

func (b Engine) untaggedPresence(ctx context.Context, read Survey, target Resource, found Presence) (Presence, error) {
	if !found.Present || found.Mends != "" || found.Replaces != "" || b.isOff(resourceManagerAPI) {
		return found, nil
	}
	tagged, named, err := b.tierTagged(ctx, read, target)
	if !named || err != nil {
		return found, err
	}
	bound, err := b.Clients.HasTierTag(ctx, read.Tier, tagged)
	if err != nil || bound {
		return found, err
	}
	found.Mends = reasonUntagged
	return found, nil
}

func (b Engine) ensureTierTag(ctx context.Context, read Survey, target Resource) error {
	if target.KindOps().taggedAtCreation {
		return nil
	}
	tagged, named, err := b.tierTagged(ctx, read, target)
	if !named || err != nil {
		return err
	}
	return b.Clients.EnsureTierTag(ctx, read.Tier, tagged)
}

func (b Engine) removeTierTag(ctx context.Context, read Survey, target Resource) error {
	tagged, named, err := b.tierTagged(ctx, read, target)
	if !named || err != nil {
		return err
	}
	return b.Clients.RemoveTierTag(ctx, read.Tier, tagged)
}

func bucketTagged(ctx context.Context, b Engine, read Survey, target Resource) (gcloud.TaggedResource, bool, error) {
	name := read.Buckets[target.Class]
	if name == "" {
		recorded, err := b.Clients.RecordedTierBuckets(ctx, read.Tier)
		if err != nil {
			return gcloud.TaggedResource{}, false, err
		}
		name = recorded[target.Class]
	}
	return b.Clients.BucketTagged(name), name != "", nil
}

func accountTagged(ctx context.Context, b Engine, _ Survey, target Resource) (gcloud.TaggedResource, bool, error) {
	found, err := b.Clients.ReadAccount(ctx, target.Name)
	if found == nil || err != nil {
		return gcloud.TaggedResource{}, false, err
	}
	return b.Clients.AccountTagged(found.UniqueId), true, nil
}

func repositoryTagged(_ context.Context, b Engine, _ Survey, target Resource) (gcloud.TaggedResource, bool, error) {
	return b.Clients.RepositoryTagged(target.Name), true, nil
}

func secretTagged(ctx context.Context, b Engine, _ Survey, target Resource) (gcloud.TaggedResource, bool, error) {
	tagged, err := b.Clients.SecretTagged(ctx, target.Name)
	return tagged, err == nil, err
}
