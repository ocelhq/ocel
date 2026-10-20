package gcloud

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"cloud.google.com/go/storage"
	"google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/iam/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
)

const (
	serviceAdmitPermission  = "run.services.update"
	accountAdmitPermission  = "iam.serviceAccounts.actAs"
	registryAdmitPermission = "artifactregistry.repositories.uploadArtifacts"
	bucketAdmitPermission   = "storage.buckets.update"
)

var tierAdmission = Patience{Attempts: 30, Ceiling: WaitCeiling}

type Admission func(ctx context.Context, tier environment.Tier) error

type notAdmitted struct{ error }

func (c *Clients) AwaitTierAdmits(ctx context.Context, tier environment.Tier, target TaggedResource) error {
	if !c.TierTagged() {
		return nil
	}
	return c.awaitAdmitted(ctx, tier, target, tierAdmission)
}

func (c *Clients) awaitAdmitted(ctx context.Context, tier environment.Tier, target TaggedResource, limits Patience) error {
	if target.Admits == nil {
		return nil
	}
	var denied error
	for attempt := range limits.Attempts {
		if attempt > 0 && !googleretry.WaitedFor(ctx, attempt, limits.Ceiling) {
			return ctx.Err()
		}
		err := target.Admits(ctx, tier)
		var pending notAdmitted
		if !errors.As(err, &pending) {
			if err != nil {
				return fmt.Errorf("ask whether this credential reaches %s through its %s tag: %w", target.Name, c.TierTagValuePath(tier), err)
			}
			return nil
		}
		denied = pending.error
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"%s carries the %s tag, and a couple of minutes later Google still refuses this credential through it: %v.\n"+
			"Deploy again once Google has caught up, and grant what it names if it still refuses",
		target.Name, c.TierTagValuePath(tier), denied)
}

func (c *Clients) unpermitted(tier environment.Tier, role, permission, path string) error {
	return notAdmitted{fmt.Errorf("the deploy credential lacks %s: Google answers that it may not %s on %s",
		c.DeployCredential(tier).Tiered(role).String(), permission, path)}
}

func holds(granted []string, permission string) bool { return slices.Contains(granted, permission) }

func (c *Clients) serviceAdmits(path string) Admission {
	return func(ctx context.Context, tier environment.Tier) error {
		service, err := c.Run()
		if err != nil {
			return err
		}
		answer, err := googleretry.Attempted(ctx, service.Projects.Locations.Services.TestIamPermissions(path,
			&run.GoogleIamV1TestIamPermissionsRequest{Permissions: []string{serviceAdmitPermission}}).Context(ctx).Do)
		if err != nil || holds(answer.Permissions, serviceAdmitPermission) {
			return err
		}
		return c.unpermitted(tier, credential.RunRole, serviceAdmitPermission, path)
	}
}

func (c *Clients) accountAdmits(path string) Admission {
	return func(ctx context.Context, tier environment.Tier) error {
		service, err := c.Accounts()
		if err != nil {
			return err
		}
		answer, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.TestIamPermissions(path,
			&iam.TestIamPermissionsRequest{Permissions: []string{accountAdmitPermission}}).Context(ctx).Do)
		if err != nil || holds(answer.Permissions, accountAdmitPermission) {
			return err
		}
		return c.unpermitted(tier, credential.RunAsRole, accountAdmitPermission, path)
	}
}

func (c *Clients) repositoryAdmits(path string) Admission {
	return func(ctx context.Context, tier environment.Tier) error {
		service, err := c.Repositories()
		if err != nil {
			return err
		}
		answer, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.TestIamPermissions(path,
			&artifactregistry.TestIamPermissionsRequest{Permissions: []string{registryAdmitPermission}}).Context(ctx).Do)
		if err != nil || holds(answer.Permissions, registryAdmitPermission) {
			return err
		}
		return c.unpermitted(tier, credential.RepositoriesRole, registryAdmitPermission, path)
	}
}

func (c *Clients) bucketAdmits(bucket string) Admission {
	return func(ctx context.Context, tier environment.Tier) error {
		client, err := c.Storage()
		if err != nil {
			return err
		}
		granted, err := googleretry.Called(ctx, func() ([]string, error) {
			return client.Bucket(bucket).Retryer(storage.WithPolicy(storage.RetryNever)).IAM().TestPermissions(ctx, []string{bucketAdmitPermission})
		})
		if err != nil || holds(granted, bucketAdmitPermission) {
			return err
		}
		return c.unpermitted(tier, credential.BucketsRole, bucketAdmitPermission, "bucket "+bucket)
	}
}

func (c *Clients) instanceAdmits(instance string) Admission {
	return func(ctx context.Context, tier environment.Tier) error {
		service, err := c.SQL()
		if err != nil {
			return err
		}
		_, err = googleretry.Attempted(ctx, service.Databases.List(c.Project(), instance).Context(ctx).Do)
		if Forbidden(err) {
			return notAdmitted{ExplainMissingGrant(fmt.Errorf("list the databases of Cloud SQL instance %s: %w", instance, err),
				c.DeployCredential(tier).Tiered(credential.InstancesRole).String())}
		}
		return err
	}
}
