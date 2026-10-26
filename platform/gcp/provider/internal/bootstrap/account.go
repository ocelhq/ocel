package bootstrap

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const reasonUngranted = "this credential may not act as it"

var accountKind = KindOps{
	api:       IAMAPI,
	tagged:    accountTagged,
	noun:      "service account",
	terraform: cost.ServiceAccountType,
	survey: func(ctx context.Context, b Engine, read Survey, target Resource) (Presence, error) {
		return b.AccountPresence(ctx, read.Tier, target.Name)
	},
	Make: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		return b.MakeAccount(ctx, read, target.Name)
	},
	Take: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		return b.TakeAccount(ctx, read.Tier, target.Name)
	},
}

const RunAsRole = "roles/iam.serviceAccountUser"

type AccountPurpose struct {
	DisplayName string
	Description string
	Ungranted   string
	Grant       func(ctx context.Context, b Engine, tier environment.Tier) error
	Forget      func(ctx context.Context, b Engine, tier environment.Tier) error
	Granted     func(ctx context.Context, b Engine, tier environment.Tier) (bool, error)
}

type Account struct {
	Name    func(names names.Names, tier environment.Tier) string
	Purpose func(b Engine, tier environment.Tier) AccountPurpose
}

func (b Engine) PurposeOf(tier environment.Tier, name string) (AccountPurpose, error) {
	for _, each := range b.Accounts {
		if each.Name(b.Clients.Names, tier) == name {
			return each.Purpose(b, tier), nil
		}
	}
	return AccountPurpose{}, fmt.Errorf("no purpose is known for the %s service account in the %s tier", name, tier)
}

func (b Engine) MakeAccount(ctx context.Context, read Survey, name string) error {
	service, err := b.Clients.Accounts()
	if err != nil {
		return err
	}
	purpose, err := b.PurposeOf(read.Tier, name)
	if err != nil {
		return err
	}
	made, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Create("projects/"+b.Clients.Project(), &iam.CreateServiceAccountRequest{
		AccountId: name,
		ServiceAccount: &iam.ServiceAccount{
			DisplayName: purpose.DisplayName,
			Description: purpose.Description,
		},
	}).Context(ctx).Do)
	if err != nil && !gcloud.Taken(err) {
		return fmt.Errorf("create the %s service account: %w", name, err)
	}
	if made == nil {
		if made, err = b.Clients.ReadAccount(ctx, name); err != nil || made == nil {
			return cmp.Or(err, fmt.Errorf("the %s service account was taken and cannot be read back", name))
		}
	}
	if err := b.Clients.EnsureTierTag(ctx, read.Tier, b.Clients.AccountTagged(made.UniqueId)); err != nil {
		return err
	}
	if err := b.GrantRunAs(ctx, name); err != nil {
		return err
	}
	return purpose.Grant(ctx, b, read.Tier)
}

func (b Engine) GrantRunAs(ctx context.Context, name string) error {
	member, err := b.Clients.Principal(ctx)
	if err != nil {
		return err
	}
	if err := gcloud.BindAccountRole(ctx, b.Clients, name, RunAsRole, gcloud.MemberOf(member), true); err != nil {
		return fmt.Errorf("let %s deploy apps that run as the %s service account: %w", member, name, err)
	}
	return nil
}

func (b Engine) TakeAccount(ctx context.Context, tier environment.Tier, name string) error {
	purpose, err := b.PurposeOf(tier, name)
	if err != nil {
		return err
	}
	if err := purpose.Forget(ctx, b, tier); err != nil {
		return err
	}
	service, err := b.Clients.Accounts()
	if err != nil {
		return err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Delete(gcloud.AccountPath(b.Clients, name)).Context(ctx).Do); err != nil && !gcloud.Absent(err) {
		return fmt.Errorf("delete the %s service account: %w", name, err)
	}
	return nil
}

func (b Engine) AccountPresence(ctx context.Context, tier environment.Tier, name string) (Presence, error) {
	service, err := b.Clients.Accounts()
	if err != nil {
		return Presence{}, err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Get(gcloud.AccountPath(b.Clients, name)).Context(ctx).Do)
	if gcloud.Absent(err) {
		return Presence{}, nil
	}
	if err != nil {
		return Presence{}, fmt.Errorf("read the %s service account: %w", name, err)
	}
	policy, err := b.accountPolicy(ctx, name)
	if err != nil {
		return Presence{}, err
	}
	member, err := b.Clients.Principal(ctx)
	if err != nil {
		return Presence{}, err
	}
	if !hasBinding(policy, RunAsRole, gcloud.MemberOf(member)) {
		return Presence{Present: true, Mends: reasonUngranted}, nil
	}
	purpose, err := b.PurposeOf(tier, name)
	if err != nil {
		return Presence{}, err
	}
	grants, err := purpose.Granted(ctx, b, tier)
	if err != nil {
		return Presence{}, err
	}
	if !grants {
		return Presence{Present: true, Mends: purpose.Ungranted}, nil
	}
	return Presence{Present: true}, nil
}

func (b Engine) accountPolicy(ctx context.Context, name string) (*iam.Policy, error) {
	service, err := b.Clients.Accounts()
	if err != nil {
		return nil, err
	}
	policy, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.GetIamPolicy(gcloud.AccountPath(b.Clients, name)).Context(ctx).Do)
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
