package gcp

import (
	"context"
	"fmt"
	"slices"
	"time"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const reasonUngranted = "it exists, and the credential bootstrapping here may not hand an app to Cloud Run to run as it"

var accountKind = kind{
	noun:      "service account",
	terraform: tfServiceAccount,
	survey: func(ctx context.Context, b bootstrap, tier environment.Tier, target item) (presence, error) {
		return b.accountPresence(ctx, tier, target.Name)
	},
	make: func(ctx context.Context, b bootstrap, read survey, target item) error {
		return b.makeAccount(ctx, read, target.Name)
	},
	take: func(ctx context.Context, b bootstrap, read survey, target item) (time.Duration, error) {
		return 0, b.takeAccount(ctx, read.Tier, target.Name)
	},
}

const runAsRole = "roles/iam.serviceAccountUser"

type accountPurpose struct {
	displayName string
	description string
	ungranted   string
	grant       func(ctx context.Context, tier environment.Tier) error
	forget      func(ctx context.Context, tier environment.Tier) error
	granted     func(ctx context.Context, tier environment.Tier) (bool, error)
}

func (b bootstrap) purposeOf(tier environment.Tier, name string) (accountPurpose, error) {
	switch name {
	case b.clients.EnvSourceSyncAccount(tier):
		return b.syncPurpose(tier), nil
	case b.clients.PushAccount(tier):
		return b.pushPurpose(tier), nil
	case b.clients.RealtimeAccount(tier):
		return b.realtimePurpose(tier), nil
	}
	return accountPurpose{}, fmt.Errorf("no purpose is known for the %s service account in the %s tier", name, tier)
}

func (b bootstrap) makeAccount(ctx context.Context, read survey, name string) error {
	service, err := b.clients.Accounts()
	if err != nil {
		return err
	}
	purpose, err := b.purposeOf(read.Tier, name)
	if err != nil {
		return err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Create("projects/"+b.clients.Project(), &iam.CreateServiceAccountRequest{
		AccountId: name,
		ServiceAccount: &iam.ServiceAccount{
			DisplayName: purpose.displayName,
			Description: purpose.description,
		},
	}).Context(ctx).Do)
	if err != nil && !gcloud.Taken(err) {
		return fmt.Errorf("create the %s service account: %w", name, err)
	}
	if err := b.grantRunAs(ctx, name); err != nil {
		return err
	}
	return purpose.grant(ctx, read.Tier)
}

func (b bootstrap) grantRunAs(ctx context.Context, name string) error {
	member, err := b.clients.Principal(ctx)
	if err != nil {
		return err
	}
	if err := gcloud.BindAccountRole(ctx, b.clients, name, runAsRole, gcloud.MemberOf(member), true); err != nil {
		return fmt.Errorf("let %s deploy apps that run as the %s service account: %w", member, name, err)
	}
	return nil
}

func (b bootstrap) takeAccount(ctx context.Context, tier environment.Tier, name string) error {
	purpose, err := b.purposeOf(tier, name)
	if err != nil {
		return err
	}
	if err := purpose.forget(ctx, tier); err != nil {
		return err
	}
	service, err := b.clients.Accounts()
	if err != nil {
		return err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Delete(gcloud.AccountPath(b.clients, name)).Context(ctx).Do); err != nil && !gcloud.Absent(err) {
		return fmt.Errorf("delete the %s service account: %w", name, err)
	}
	return nil
}

func (b bootstrap) accountPresence(ctx context.Context, tier environment.Tier, name string) (presence, error) {
	service, err := b.clients.Accounts()
	if err != nil {
		return presence{}, err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Get(gcloud.AccountPath(b.clients, name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return presence{}, nil
	}
	if err != nil {
		return presence{}, fmt.Errorf("read the %s service account: %w", name, err)
	}
	policy, err := b.accountPolicy(ctx, name)
	if err != nil {
		return presence{}, err
	}
	member, err := b.clients.Principal(ctx)
	if err != nil {
		return presence{}, err
	}
	if !hasBinding(policy, runAsRole, gcloud.MemberOf(member)) {
		return presence{present: true, mends: reasonUngranted}, nil
	}
	purpose, err := b.purposeOf(tier, name)
	if err != nil {
		return presence{}, err
	}
	grants, err := purpose.granted(ctx, tier)
	if err != nil {
		return presence{}, err
	}
	if !grants {
		return presence{present: true, mends: purpose.ungranted}, nil
	}
	return presence{present: true}, nil
}

func (b bootstrap) accountPolicy(ctx context.Context, name string) (*iam.Policy, error) {
	service, err := b.clients.Accounts()
	if err != nil {
		return nil, err
	}
	policy, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.GetIamPolicy(gcloud.AccountPath(b.clients, name)).Context(ctx).Do)
	if err != nil {
		return nil, fmt.Errorf("read who may run apps as the %s service account: %w", name, err)
	}
	return policy, nil
}

func hasBinding(policy *iam.Policy, role, member string) bool {
	for _, binding := range policy.Bindings {
		if binding.Role == role && binding.Condition == nil && slices.Contains(binding.Members, member) {
			return true
		}
	}
	return false
}
