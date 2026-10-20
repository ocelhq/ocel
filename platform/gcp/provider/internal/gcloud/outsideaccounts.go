package gcloud

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/iam/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

const RootOutsideAccounts records.Root = "outsideaccounts"

const (
	outsideAccountsEntry = "tagged"
	accountsPerPage      = 100
	namedAccountsShown   = 5
)

func OutsideAccountsKey(tier environment.Tier) records.Key {
	return records.Partition{Tier: tier, Root: RootOutsideAccounts}.Key(outsideAccountsEntry)
}

func (c *Clients) ListAccounts(ctx context.Context) ([]*iam.ServiceAccount, error) {
	service, err := c.Accounts()
	if err != nil {
		return nil, err
	}
	var listed []*iam.ServiceAccount
	token := ""
	for {
		call := service.Projects.ServiceAccounts.List("projects/" + c.Project()).PageSize(accountsPerPage).Context(ctx)
		if token != "" {
			call = call.PageToken(token)
		}
		page, err := googleretry.Attempted(ctx, call.Do)
		if err != nil {
			return nil, ExplainMissingGrant(fmt.Errorf("list the service accounts of project %s: %w", c.Project(), err), "iam.serviceAccounts.list on the project")
		}
		listed = append(listed, page.Accounts...)
		if token = page.NextPageToken; token == "" {
			return listed, nil
		}
	}
}

func (c *Clients) isOutsideAccount(account *iam.ServiceAccount) bool {
	id, inProject := c.AccountID(account)
	return !inProject || !names.IsOcelAccount(id)
}

type OutsideAccounts struct {
	Untagged []*iam.ServiceAccount
	Tagged   []string
}

func (o OutsideAccounts) UntaggedNamed() string {
	emails := make([]string, 0, namedAccountsShown)
	for _, account := range o.Untagged[:min(len(o.Untagged), namedAccountsShown)] {
		emails = append(emails, account.Email)
	}
	if more := len(o.Untagged) - len(emails); more > 0 {
		return strings.Join(emails, ", ") + fmt.Sprintf(" and %d more", more)
	}
	return strings.Join(emails, ", ")
}

func (c *Clients) ReadOutsideAccounts(ctx context.Context, known []string) (OutsideAccounts, error) {
	var read OutsideAccounts
	listed, err := c.ListAccounts(ctx)
	if err != nil {
		return OutsideAccounts{}, err
	}
	for _, account := range listed {
		if !c.isOutsideAccount(account) || slices.Contains(known, account.UniqueId) {
			continue
		}
		tagged, err := c.CarriesTierTag(ctx, c.AccountTagged(account.UniqueId))
		if Forbidden(err) {
			tagged, err = true, nil
		}
		if err != nil {
			return OutsideAccounts{}, err
		}
		if tagged {
			read.Tagged = append(read.Tagged, account.UniqueId)
			continue
		}
		read.Untagged = append(read.Untagged, account)
	}
	return read, nil
}

func (c *Clients) RecordedOutsideAccounts(ctx context.Context, tier environment.Tier) ([]string, error) {
	if c.Records == nil {
		return nil, nil
	}
	entry, err := records.ReadOrEmpty(ctx, c.Records, OutsideAccountsKey(tier))
	if err != nil || len(entry.Value) == 0 {
		return nil, err
	}
	var recorded []string
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return nil, fmt.Errorf("read the service accounts the %s bootstrap found carrying an ocel-tier tag: %w", tier, err)
	}
	return recorded, nil
}

func (c *Clients) RecordOutsideAccounts(ctx context.Context, tier environment.Tier, tagged []string) error {
	if c.Records == nil {
		return fmt.Errorf("gcp: these clients hold no record store, so the accounts the %s bootstrap tagged cannot be recorded", tier)
	}
	sorted := slices.Sorted(slices.Values(tagged))
	encoded, err := json.Marshal(slices.Compact(sorted))
	if err != nil {
		return err
	}
	return records.Change(ctx, c.Records, OutsideAccountsKey(tier), func(recorded records.Entry) ([]byte, bool, error) {
		return encoded, string(recorded.Value) != string(encoded), nil
	})
}
