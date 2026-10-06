package gcp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"cloud.google.com/go/storage"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const bucketObjectsRole = "roles/storage.objectAdmin"

func grantedBuckets(app *provider.AppSpec) []string {
	var granted []string
	for _, binding := range slices.Concat(app.Values.Bindings, app.Grants) {
		name := binding.Properties[provider.PropertyBucket]
		if binding.Type == provider.BindingBucket && !binding.Endpointed() && name != "" && !slices.Contains(granted, name) {
			granted = append(granted, name)
		}
	}
	return granted
}

func (c *clients) bucketGrants(ctx context.Context, spec provider.StackSpec, member string) []func() error {
	buckets := grantedBuckets(spec.App)
	if len(buckets) == 0 {
		return nil
	}
	grants := make([]func() error, 0, len(buckets)+1)
	for _, bucket := range buckets {
		grants = append(grants, func() error {
			_, err := c.bindBucketRole(ctx, bucket, member, true)
			return err
		})
	}
	return append(grants, func() error {
		own := c.AppAccount(spec.Ref.Tier, spec.Ref.Project, spec.App.App)
		return wrapAccountGrantError(c.bindAccountRole(ctx, own, tokenCreatorRole, member, true), c.AppAccountsRolePath())
	})
}

func (c *clients) bindBucketRole(ctx context.Context, bucket, member string, granting bool) (bool, error) {
	client, err := c.Storage()
	if err != nil {
		return false, err
	}
	handle := client.Bucket(bucket).IAM().V3()
	var refused error
	for attempt := range bindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return false, ctx.Err()
		}
		policy, err := handle.Policy(ctx)
		if err != nil {
			return false, fmt.Errorf("read who may use the Cloud Storage bucket %s: %w", bucket, err)
		}
		bindings, changed := boundKeyMember(policy.Bindings, bucketObjectsRole, member, granting)
		if !changed {
			return false, nil
		}
		policy.Bindings = bindings
		if refused = handle.SetPolicy(ctx, policy); refused == nil {
			return true, nil
		}
		if !stale(refused) {
			break
		}
	}
	return false, fmt.Errorf("let %s use the objects of the Cloud Storage bucket %s: %w", member, bucket, refused)
}

func (c *clients) revokeBucketAccess(ctx context.Context, recorded []stackrecords.NamedStack, member string, keeping []string) ([]string, error) {
	var revoked []string
	for _, stack := range recorded {
		if !stack.Name.IsInfra() {
			continue
		}
		for _, binding := range stack.Bindings {
			bucket := binding.Properties[provider.PropertyBucket]
			if binding.Type != provider.BindingBucket || bucket == "" || slices.Contains(revoked, bucket) || slices.Contains(keeping, bucket) {
				continue
			}
			changed, err := c.bindBucketRole(ctx, bucket, member, false)
			if errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if changed {
				revoked = append(revoked, bucket)
			}
		}
	}
	return revoked, nil
}

func (p *Provider) revokeDroppedBuckets(ctx context.Context, c *clients, spec provider.StackSpec, member string) error {
	infra := naming.InfraStack(spec.Ref.Name.Env)
	recorded, found, err := stackrecords.Read(ctx, p.KeyValues(), spec.Ref.Tier, spec.Ref.Project, infra)
	if err != nil || !found {
		return err
	}
	_, err = c.revokeBucketAccess(ctx, []stackrecords.NamedStack{{Name: infra, Stack: recorded}}, member, grantedBuckets(spec.App))
	return err
}
