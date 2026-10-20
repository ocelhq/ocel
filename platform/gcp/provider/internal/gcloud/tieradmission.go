package gcloud

import (
	"context"
	"fmt"
	"slices"

	"cloud.google.com/go/storage"
	"google.golang.org/api/artifactregistry/v1"
	"google.golang.org/api/iam/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	tierAdmittedAttempts    = 30
	serviceAdmitPermission  = "run.services.update"
	accountAdmitPermission  = "iam.serviceAccounts.actAs"
	registryAdmitPermission = "artifactregistry.repositories.uploadArtifacts"
	bucketAdmitPermission   = "storage.buckets.update"
)

type Admission func(ctx context.Context) (bool, error)

func (c *Clients) awaitTierAdmits(ctx context.Context, tier environment.Tier, target TaggedResource) error {
	if target.Admits == nil {
		return nil
	}
	for attempt := range tierAdmittedAttempts {
		if attempt > 0 && !googleretry.WaitedFor(ctx, attempt, WaitCeiling) {
			return ctx.Err()
		}
		admitted, err := target.Admits(ctx)
		if err != nil {
			return fmt.Errorf("ask whether this credential reaches %s through its %s tag: %w", target.Name, c.TierTagValuePath(tier), err)
		}
		if admitted {
			return nil
		}
	}
	return refuseUnadmitted(c, tier, target)
}

func (c *Clients) RetriedUntilTierAdmits(ctx context.Context, tier environment.Tier, target TaggedResource, call func() error) error {
	for attempt := range tierAdmittedAttempts {
		if attempt > 0 && !googleretry.WaitedFor(ctx, attempt, WaitCeiling) {
			return ctx.Err()
		}
		if err := call(); !c.TierTagged() || !Forbidden(err) {
			return err
		}
	}
	return refuseUnadmitted(c, tier, target)
}

func refuseUnadmitted(c *Clients, tier environment.Tier, target TaggedResource) error {
	return refusal.Refuse(refusal.CodeNotReady,
		"%s carries the %s tag, and Google still does not let this credential reach it through that tag a couple of minutes later.\n"+
			"Deploy again once Google has caught up",
		target.Name, c.TierTagValuePath(tier))
}

func holds(granted []string, permission string) bool { return slices.Contains(granted, permission) }

func (c *Clients) serviceAdmits(path string) Admission {
	return func(ctx context.Context) (bool, error) {
		service, err := c.Run()
		if err != nil {
			return false, err
		}
		answer, err := googleretry.Attempted(ctx, service.Projects.Locations.Services.TestIamPermissions(path,
			&run.GoogleIamV1TestIamPermissionsRequest{Permissions: []string{serviceAdmitPermission}}).Context(ctx).Do)
		if err != nil {
			return false, err
		}
		return holds(answer.Permissions, serviceAdmitPermission), nil
	}
}

func (c *Clients) accountAdmits(path string) Admission {
	return func(ctx context.Context) (bool, error) {
		service, err := c.Accounts()
		if err != nil {
			return false, err
		}
		answer, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.TestIamPermissions(path,
			&iam.TestIamPermissionsRequest{Permissions: []string{accountAdmitPermission}}).Context(ctx).Do)
		if err != nil {
			return false, err
		}
		return holds(answer.Permissions, accountAdmitPermission), nil
	}
}

func (c *Clients) repositoryAdmits(path string) Admission {
	return func(ctx context.Context) (bool, error) {
		service, err := c.Repositories()
		if err != nil {
			return false, err
		}
		answer, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.TestIamPermissions(path,
			&artifactregistry.TestIamPermissionsRequest{Permissions: []string{registryAdmitPermission}}).Context(ctx).Do)
		if err != nil {
			return false, err
		}
		return holds(answer.Permissions, registryAdmitPermission), nil
	}
}

func (c *Clients) bucketAdmits(bucket string) Admission {
	return func(ctx context.Context) (bool, error) {
		client, err := c.Storage()
		if err != nil {
			return false, err
		}
		granted, err := googleretry.Called(ctx, func() ([]string, error) {
			return client.Bucket(bucket).Retryer(storage.WithPolicy(storage.RetryNever)).IAM().TestPermissions(ctx, []string{bucketAdmitPermission})
		})
		if err != nil {
			return false, err
		}
		return holds(granted, bucketAdmitPermission), nil
	}
}
