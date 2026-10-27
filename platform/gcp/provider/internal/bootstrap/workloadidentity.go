package bootstrap

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	identityDeleted       = "DELETED"
	identityProviderMask  = "displayName,disabled,attributeMapping,attributeCondition,oidc"
	reasonIdentityDeleted = "it was deleted"
	reasonTrustChanged    = "the trust it holds changed"
)

func (b Engine) identityProviderPath(id string) string {
	return b.Clients.ConsolePoolPath() + "/providers/" + id
}

func (b Engine) readPool(ctx context.Context) (*iam.WorkloadIdentityPool, error) {
	service, err := b.Clients.Accounts()
	if err != nil {
		return nil, err
	}
	pool, err := googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Get(b.Clients.ConsolePoolPath()).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s workload identity pool: %w", b.Clients.ConsolePool(), err)
	}
	return pool, nil
}

func (b Engine) makePool(ctx context.Context) error {
	pool, err := b.readPool(ctx)
	if err != nil {
		return err
	}
	service, err := b.Clients.Accounts()
	if err != nil {
		return err
	}
	id := b.Clients.ConsolePool()
	if pool == nil {
		started, err := googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Create("projects/"+b.Clients.Project()+"/locations/global", &iam.WorkloadIdentityPool{
			DisplayName: "ocel console",
			Description: "the console's tokens, exchanged for this project's deploy service accounts",
		}).WorkloadIdentityPoolId(id).Context(ctx).Do)
		if !gcloud.Taken(err) {
			return b.awaitIdentity(ctx, "creating the "+id+" workload identity pool", started, err)
		}
		if pool, err = b.readPool(ctx); err != nil || pool == nil {
			return cmp.Or(err, fmt.Errorf("the %s workload identity pool was taken and cannot be read back", id))
		}
	}
	if pool.State != identityDeleted {
		return nil
	}
	started, err := googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Undelete(b.Clients.ConsolePoolPath(), &iam.UndeleteWorkloadIdentityPoolRequest{}).Context(ctx).Do)
	return b.awaitIdentity(ctx, "undeleting the "+id+" workload identity pool", started, err)
}

func (b Engine) deletePool(ctx context.Context) error {
	service, err := b.Clients.Accounts()
	if err != nil {
		return err
	}
	started, err := googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Delete(b.Clients.ConsolePoolPath()).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil
	}
	return b.awaitIdentity(ctx, "deleting the "+b.Clients.ConsolePool()+" workload identity pool", started, err)
}

func (b Engine) readIdentityProvider(ctx context.Context, id string) (*iam.WorkloadIdentityPoolProvider, error) {
	service, err := b.Clients.Accounts()
	if err != nil {
		return nil, err
	}
	found, err := googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Providers.Get(b.identityProviderPath(id)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s workload identity provider: %w", id, err)
	}
	return found, nil
}

func identityProviderDrift(current, want *iam.WorkloadIdentityPoolProvider) string {
	switch {
	case current.State == identityDeleted:
		return reasonIdentityDeleted
	case current.Disabled || current.AttributeCondition != want.AttributeCondition ||
		!maps.Equal(current.AttributeMapping, want.AttributeMapping) || current.Oidc == nil ||
		current.Oidc.IssuerUri != want.Oidc.IssuerUri || !slices.Equal(current.Oidc.AllowedAudiences, want.Oidc.AllowedAudiences):
		return reasonTrustChanged
	}
	return ""
}

func (b Engine) makeIdentityProvider(ctx context.Context, id string, want *iam.WorkloadIdentityPoolProvider) error {
	current, err := b.readIdentityProvider(ctx, id)
	if err != nil {
		return err
	}
	service, err := b.Clients.Accounts()
	if err != nil {
		return err
	}
	if current == nil {
		started, err := googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Providers.Create(b.Clients.ConsolePoolPath(), want).
			WorkloadIdentityPoolProviderId(id).Context(ctx).Do)
		if !gcloud.Taken(err) {
			return b.awaitIdentity(ctx, "creating the "+id+" workload identity provider", started, err)
		}
		if current, err = b.readIdentityProvider(ctx, id); err != nil || current == nil {
			return cmp.Or(err, fmt.Errorf("the %s workload identity provider was taken and cannot be read back", id))
		}
	}
	drift := identityProviderDrift(current, want)
	if drift == reasonIdentityDeleted {
		started, err := googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Providers.Undelete(b.identityProviderPath(id), &iam.UndeleteWorkloadIdentityPoolProviderRequest{}).Context(ctx).Do)
		if err := b.awaitIdentity(ctx, "undeleting the "+id+" workload identity provider", started, err); err != nil {
			return err
		}
	}
	if drift == "" {
		return nil
	}
	patched := *want
	patched.ForceSendFields = []string{"Disabled", "AttributeCondition"}
	started, err := googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Providers.Patch(b.identityProviderPath(id), &patched).
		UpdateMask(identityProviderMask).Context(ctx).Do)
	return b.awaitIdentity(ctx, "updating the "+id+" workload identity provider", started, err)
}

func (b Engine) deleteIdentityProvider(ctx context.Context, id string) error {
	service, err := b.Clients.Accounts()
	if err != nil {
		return err
	}
	started, err := googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Providers.Delete(b.identityProviderPath(id)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return nil
	}
	return b.awaitIdentity(ctx, "deleting the "+id+" workload identity provider", started, err)
}

func (b Engine) awaitIdentity(ctx context.Context, doing string, started *iam.Operation, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}
	service, err := b.Clients.Accounts()
	if err != nil {
		return err
	}
	_, err = gcloud.AwaitOperation(ctx, gcloud.Patience{Attempts: gcloud.WaitAttempts, Ceiling: gcloud.WaitCeiling}, "IAM", doing, started,
		identityOutcome,
		func(name string) (*iam.Operation, error) {
			return googleretry.Attempted(ctx, service.Projects.Locations.WorkloadIdentityPools.Operations.Get(name).Context(ctx).Do)
		})
	return err
}

func identityOutcome(op *iam.Operation) gcloud.OperationOutcome {
	outcome := gcloud.OperationOutcome{Name: op.Name, Done: op.Done}
	if op.Error != nil {
		outcome.Failure = gcloud.FailureOf(op.Error.Code, op.Error.Message)
	}
	return outcome
}
