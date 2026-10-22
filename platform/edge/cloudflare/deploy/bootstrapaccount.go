package cloudflare

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const bootstrapAccountRecord = "bootstrap-account"

type bootstrapAccount struct {
	ID string `json:"id"`
}

func bootstrapAccountKey(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(Kind), bootstrapAccountRecord)
}

func decodeBootstrapAccount(entry records.Entry) (bootstrapAccount, error) {
	var recorded bootstrapAccount
	if len(entry.Value) == 0 {
		return recorded, nil
	}
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return bootstrapAccount{}, fmt.Errorf("read %s: %w", entry.Key, err)
	}
	return recorded, nil
}

func refuseOtherAccount(tier environment.Tier, recorded, accountID, doing string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"cannot %s: this origin bootstrapped its %s tier on Cloudflare against account %s, but %s names account %s\n"+
			"Set %s back to %s, or move the tier to account %s: run `ocel bootstrap destroy %s` while %s is still %s, then bootstrap the tier again under %s",
		doing, tier, recorded, envAccountID, accountID,
		envAccountID, recorded, accountID, tier, envAccountID, recorded, accountID)
}

func (o *resolvedOrigin) readBootstrapAccount(ctx context.Context, tier environment.Tier) (string, error) {
	o.accountsMu.Lock()
	remembered, held := o.recordedAccounts[tier]
	o.accountsMu.Unlock()
	if held {
		return remembered, nil
	}
	store, err := o.requireRecords("read the Cloudflare account tier " + string(tier) + " was bootstrapped against")
	if err != nil {
		return "", err
	}
	entry, err := records.ReadOrEmpty(ctx, store, bootstrapAccountKey(tier))
	if err != nil {
		return "", fmt.Errorf("read the Cloudflare account tier %s was bootstrapped against: %w", tier, err)
	}
	recorded, err := decodeBootstrapAccount(entry)
	if err != nil {
		return "", err
	}
	if recorded.ID != "" {
		o.rememberAccount(tier, recorded.ID)
	}
	return recorded.ID, nil
}

func (o *resolvedOrigin) rememberAccount(tier environment.Tier, accountID string) {
	o.accountsMu.Lock()
	defer o.accountsMu.Unlock()
	if o.recordedAccounts == nil {
		o.recordedAccounts = map[environment.Tier]string{}
	}
	if accountID == "" {
		delete(o.recordedAccounts, tier)
		return
	}
	o.recordedAccounts[tier] = accountID
}

func (o *resolvedOrigin) refuseAccountChange(ctx context.Context, tier environment.Tier, accountID, doing string) error {
	recorded, err := o.readBootstrapAccount(ctx, tier)
	if err != nil {
		return err
	}
	if recorded != "" && recorded != accountID {
		return refuseOtherAccount(tier, recorded, accountID, doing)
	}
	return nil
}

func (o *resolvedOrigin) requireTierAccount(ctx context.Context, tier environment.Tier, doing string) (string, error) {
	accountID, err := requireAccountID(doing)
	if err != nil {
		return "", err
	}
	return accountID, o.refuseAccountChange(ctx, tier, accountID, doing)
}

func (o *resolvedOrigin) requireBootstrappedAccount(ctx context.Context, doing string) (string, error) {
	accountID, err := requireAccountID(doing)
	if err != nil {
		return "", err
	}
	return accountID, o.refuseUnbootstrappedAccount(ctx, accountID, doing)
}

func (o *resolvedOrigin) refuseUnbootstrappedAccount(ctx context.Context, accountID, doing string) error {
	var other error
	for _, tier := range workerTiers {
		recorded, err := o.readBootstrapAccount(ctx, tier)
		if err != nil {
			return err
		}
		switch recorded {
		case accountID:
			return nil
		case "":
		default:
			if other == nil {
				other = refuseOtherAccount(tier, recorded, accountID, doing)
			}
		}
	}
	return other
}

func (o *resolvedOrigin) recordBootstrapAccount(ctx context.Context, tier environment.Tier, accountID string) error {
	doing := fmt.Sprintf("bootstrap tier %s on Cloudflare", tier)
	store, err := o.requireRecords(doing)
	if err != nil {
		return err
	}
	err = records.Change(ctx, store, bootstrapAccountKey(tier), func(entry records.Entry) ([]byte, bool, error) {
		recorded, err := decodeBootstrapAccount(entry)
		switch {
		case err != nil:
			return nil, false, err
		case recorded.ID == accountID:
			return nil, false, nil
		case recorded.ID != "":
			return nil, false, refuseOtherAccount(tier, recorded.ID, accountID, doing)
		}
		value, err := json.Marshal(bootstrapAccount{ID: accountID})
		return value, true, err
	})
	if err != nil {
		return err
	}
	o.rememberAccount(tier, accountID)
	return nil
}

func (o *resolvedOrigin) forgetBootstrapAccount(ctx context.Context, tier environment.Tier) error {
	store, err := o.requireRecords("forget the Cloudflare account tier " + string(tier) + " was bootstrapped against")
	if err != nil {
		return err
	}
	if err := records.Forget(ctx, store, bootstrapAccountKey(tier)); err != nil {
		return err
	}
	o.rememberAccount(tier, "")
	return nil
}
