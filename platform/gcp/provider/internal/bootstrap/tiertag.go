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
)

const (
	ReasonTierTagKept     = "every namespace and both tiers of this project bind their resources to its values, and Google refuses to delete a value a resource, even a deleted one, is still bound to"
	reasonTierValueAbsent = "it lacks the value of a tier, which that tier's resources would carry"
	reasonUntagged        = "it carries no ocel-tier tag, and the deploy credential is fenced to its tier by that tag"

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
	service, err := b.Clients.TagService(gcloud.GlobalTagLocation)
	if err != nil {
		return nil, err
	}
	key, err := googleretry.Attempted(ctx, service.TagKeys.GetNamespaced().Name(b.Clients.TierTagKeyPath()).Context(ctx).Do)
	if err == nil {
		return key, nil
	}
	_, err = b.tagAbsent(ctx, service, err, b.Clients.TierTagKeyPath(), tagKeyReadPermission)
	return nil, err
}

func (b Engine) tagAbsent(ctx context.Context, service *crm.Service, err error, path, permission string) (bool, error) {
	switch {
	case gcloud.Absent(err):
		return true, nil
	case !gcloud.Forbidden(err):
		return false, fmt.Errorf("read the %s tag: %w", path, err)
	}
	project := "projects/" + b.Clients.Project()
	answer, err := googleretry.Attempted(ctx, service.Projects.TestIamPermissions(project,
		&crm.TestIamPermissionsRequest{Permissions: []string{permission}}).Context(ctx).Do)
	if err != nil {
		return false, fmt.Errorf("ask %s whether this credential may read its tags: %w", project, err)
	}
	if slices.Contains(answer.Permissions, permission) {
		return true, nil
	}
	return false, refusal.Refuse(refusal.CodeDenied,
		"this credential lacks %s on project %s, and Google answers a tag it may not read as it answers a tag that does not exist, so whether %s exists is unknown.\n"+
			"Granting roles/resourcemanager.tagAdmin, which a bootstrap credential holds, covers it",
		permission, b.Clients.Project(), path)
}

func (b Engine) missingTierValues(ctx context.Context) ([]environment.Tier, error) {
	service, err := b.Clients.TagService(gcloud.GlobalTagLocation)
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
		absent, err := b.tagAbsent(ctx, service, err, path, tagValueReadPermission)
		if err != nil {
			return nil, err
		}
		if absent {
			missing = append(missing, tier)
		}
	}
	return missing, nil
}

func (b Engine) makeTierTag(ctx context.Context) error {
	if !b.Clients.TierTagged() {
		return nil
	}
	service, err := b.Clients.TagService(gcloud.GlobalTagLocation)
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
		started, err := googleretry.Attempted(ctx, service.TagValues.Create(&crm.TagValue{
			Parent:      key.Name,
			ShortName:   string(tier),
			Description: "carried by every resource ocel makes for the " + string(tier) + " tier",
		}).Context(ctx).Do)
		if gcloud.Taken(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create the %s tag value: %w", b.Clients.TierTagValuePath(tier), err)
		}
		if err := gcloud.TagOperationDone(ctx, service, "creating the "+b.Clients.TierTagValuePath(tier)+" tag value", started); err != nil {
			return err
		}
	}
	return nil
}

func (b Engine) createTierTagKey(ctx context.Context, service *crm.Service) (*crm.TagKey, error) {
	started, err := googleretry.Attempted(ctx, service.TagKeys.Create(&crm.TagKey{
		Parent:      "projects/" + b.Clients.Project(),
		ShortName:   gcloud.TierTagKey,
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
	if !found.Present || found.Mends != "" || found.Replaces != "" {
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
