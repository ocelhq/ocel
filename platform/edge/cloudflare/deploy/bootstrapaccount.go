package cloudflare

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func refuseOtherAccount(tier environment.Tier, recorded, accountID, doing string) error {
	return refusal.Refuse(refusal.CodeInvalid,
		"cannot %s: this origin bootstrapped its %s tier on Cloudflare against account %s, but %s names account %s\n"+
			"Set %s back to %s, or move the tier to account %s: run `ocel bootstrap destroy %s` while %s is still %s, then bootstrap the tier again under %s",
		doing, tier, recorded, envAccountID, accountID,
		envAccountID, recorded, accountID, tier, envAccountID, recorded, accountID)
}

func refuseUnsetAccount(doing string) error {
	return fmt.Errorf("%s is not set; it is required to %s", envAccountID, doing)
}

func (o *resolvedOrigin) readBootstrapAccount(ctx context.Context, tier environment.Tier) (string, error) {
	o.accountsMu.Lock()
	remembered, held := o.recordedAccounts[tier]
	o.accountsMu.Unlock()
	if held {
		return remembered, nil
	}
	recorded, err := o.readTierRecord(ctx, tier)
	if err != nil {
		return "", err
	}
	if recorded.AccountID != "" {
		o.rememberAccount(tier, recorded.AccountID)
	}
	return recorded.AccountID, nil
}

func (o *resolvedOrigin) rememberAccount(tier environment.Tier, accountID string) {
	o.accountsMu.Lock()
	defer o.accountsMu.Unlock()
	if o.recordedAccounts == nil {
		o.recordedAccounts = map[environment.Tier]string{}
	}
	o.recordedAccounts[tier] = accountID
}

func (o *resolvedOrigin) forgetAccount(tier environment.Tier) {
	o.accountsMu.Lock()
	defer o.accountsMu.Unlock()
	delete(o.recordedAccounts, tier)
}

func (o *resolvedOrigin) readTierAccount(ctx context.Context, tier environment.Tier, doing string) (string, error) {
	accountID := readAccountID()
	if accountID == "" {
		return "", refuseUnsetAccount(doing)
	}
	recorded, err := o.readBootstrapAccount(ctx, tier)
	if err != nil {
		return "", err
	}
	if recorded != "" && recorded != accountID {
		return "", refuseOtherAccount(tier, recorded, accountID, doing)
	}
	return accountID, nil
}

func (o *resolvedOrigin) readAnyTierAccount(ctx context.Context, doing string) (string, error) {
	accountID := readAccountID()
	if accountID == "" {
		return "", refuseUnsetAccount(doing)
	}
	var other error
	for _, tier := range workerTiers {
		recorded, err := o.readBootstrapAccount(ctx, tier)
		if err != nil {
			return "", err
		}
		switch recorded {
		case accountID:
			return accountID, nil
		case "":
		default:
			if other == nil {
				other = refuseOtherAccount(tier, recorded, accountID, doing)
			}
		}
	}
	if other != nil {
		return "", other
	}
	return accountID, nil
}

func (o *resolvedOrigin) recordBootstrapAccount(ctx context.Context, tier environment.Tier, accountID string) error {
	doing := fmt.Sprintf("bootstrap tier %s on Cloudflare", tier)
	err := o.changeTierRecord(ctx, tier, doing, func(recorded *tierRecord) (bool, error) {
		switch recorded.AccountID {
		case accountID:
			return false, nil
		case "":
			recorded.AccountID = accountID
			return true, nil
		}
		return false, refuseOtherAccount(tier, recorded.AccountID, accountID, doing)
	})
	if err != nil {
		return err
	}
	o.rememberAccount(tier, accountID)
	return nil
}
