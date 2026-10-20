package gcloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"google.golang.org/api/iam/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	appAccountVisibleAttempts = 20
	MaxDisplayNameBytes       = 100
	accountQuotaMessage       = "Maximum number of service accounts"
)

func (c *Clients) AccountExists(ctx context.Context, account string) (bool, error) {
	found, err := c.ReadAccount(ctx, account)
	return found != nil, err
}

func (c *Clients) ReadAccount(ctx context.Context, account string) (*iam.ServiceAccount, error) {
	service, err := c.Accounts()
	if err != nil {
		return nil, err
	}
	found, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Get(AccountPath(c, account)).Context(ctx).Do)
	if Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s service account: %w", account, err)
	}
	return found, nil
}

func (c *Clients) CreateAccount(ctx context.Context, tier environment.Tier, account, displayName, description string) error {
	found, err := c.ReadAccount(ctx, account)
	if err != nil {
		return ExplainMissingGrant(err, c.AppAccountsRolePath())
	}
	if found == nil {
		if found, err = c.makeAccount(ctx, account, displayName, description); err != nil {
			return err
		}
	}
	return c.EnsureTierTag(ctx, tier, c.AccountTagged(found.UniqueId))
}

func (c *Clients) makeAccount(ctx context.Context, account, displayName, description string) (*iam.ServiceAccount, error) {
	service, err := c.Accounts()
	if err != nil {
		return nil, err
	}
	made, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Create("projects/"+c.Project(), &iam.CreateServiceAccountRequest{
		AccountId:      account,
		ServiceAccount: &iam.ServiceAccount{DisplayName: displayName, Description: description},
	}).Context(ctx).Do)
	switch {
	case err == nil:
		return made, nil
	case Taken(err):
		return c.readTakenAccount(ctx, account)
	case googleretry.AnsweredCode(err) == http.StatusTooManyRequests || strings.Contains(err.Error(), accountQuotaMessage):
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"project %s has as many service accounts as its Service Account Count quota allows, and every app ocel deploys runs as one of its own.\n"+
				"Delete accounts nothing uses or raise the quota in the console, then deploy again", c.Project())
	}
	return nil, ExplainMissingGrant(fmt.Errorf("create the %s service account: %w", account, err), c.AppAccountsRolePath())
}

func (c *Clients) readTakenAccount(ctx context.Context, account string) (*iam.ServiceAccount, error) {
	var found *iam.ServiceAccount
	err := UntilVisible(ctx, func() error {
		var err error
		if found, err = c.ReadAccount(ctx, account); err == nil && found == nil {
			return fmt.Errorf("%w: the %s service account answered that it exists", errUnseenAccount, account)
		}
		return err
	})
	return found, err
}

func (c *Clients) DeleteAccount(ctx context.Context, tier environment.Tier, id string) (bool, error) {
	if err := c.untagAccount(ctx, tier, id); err != nil {
		return false, err
	}
	service, err := c.Accounts()
	if err != nil {
		return false, err
	}
	if _, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Delete(AccountPath(c, id)).Context(ctx).Do); err != nil {
		if Absent(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete the %s service account: %w", id, err)
	}
	return true, nil
}

func Clipped(text string, maxBytes int) string {
	for len(text) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}

func UntilVisible(ctx context.Context, grant func() error) error {
	var err error
	for attempt := range appAccountVisibleAttempts {
		if attempt > 0 && !googleretry.WaitedFor(ctx, attempt, WaitCeiling) {
			return ctx.Err()
		}
		if err = grant(); !isUnseenAccount(err) {
			return err
		}
	}
	return err
}

var errUnseenAccount = errors.New("the service account is not readable yet")

func WrapAccountGrantError(err error, grant string) error {
	if Absent(err) {
		return fmt.Errorf("%w: %w", errUnseenAccount, err)
	}
	return ExplainMissingGrant(err, grant)
}

func isUnseenAccount(err error) bool {
	if errors.Is(err, errUnseenAccount) {
		return true
	}
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		return false
	}
	return googleretry.AnsweredCode(err) == http.StatusBadRequest || status.Code(err) == codes.InvalidArgument
}

func ExplainMissingGrant(err error, grant string) error {
	if err == nil {
		return nil
	}
	var coded interface{ GRPCStatus() *status.Status }
	if googleretry.AnsweredCode(err) == http.StatusForbidden || errors.As(err, &coded) && coded.GRPCStatus().Code() == codes.PermissionDenied {
		return fmt.Errorf("the deploy credential lacks %s: %w", grant, err)
	}
	return err
}

func (c *Clients) untagAccount(ctx context.Context, tier environment.Tier, id string) error {
	return c.RemoveTierTagIfFound(ctx, tier, func(ctx context.Context) (TaggedResource, bool, error) {
		found, err := c.ReadAccount(ctx, id)
		if found == nil || err != nil {
			return TaggedResource{}, false, err
		}
		return c.AccountTagged(found.UniqueId), true, nil
	})
}
