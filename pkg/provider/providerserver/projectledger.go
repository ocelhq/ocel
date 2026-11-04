package providerserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/seal"
)

type projectLedger struct {
	*ledger.Ledger
	cipher seal.Cipher
	tier   environment.Tier
	slug   string
}

func openProjectLedger(p provider.Provider, tier environment.Tier, slug string) projectLedger {
	return projectLedger{Ledger: ledger.New(p.KeyValues(), tier, slug), cipher: p.Cipher(), tier: tier, slug: slug}
}

func (l projectLedger) newSecretsAssociatedData(app, release string) seal.AssociatedData {
	return seal.BindToProject(l.slug, releaseSecretsPurpose, app, release)
}

func (l projectLedger) putStaged(ctx context.Context, record router.ReleaseRecord) error {
	if !record.Secrets.IsZero() {
		plain, err := json.Marshal(record.Secrets)
		if err != nil {
			return fmt.Errorf("encode the secrets %s/%s serves with: %w", record.App, record.Release, err)
		}
		if record.Sealed, err = l.cipher.Seal(ctx, l.tier, l.newSecretsAssociatedData(record.App, record.Release), plain); err != nil {
			return fmt.Errorf("seal the secrets %s/%s serves with before the ledger keeps them: %w", record.App, record.Release, err)
		}
		record.Secrets = router.ReleaseSecrets{}
	}
	return l.PutStaged(ctx, record)
}

func (l projectLedger) readRecords(ctx context.Context, promotion router.Promotion, apps []string) (map[string]router.ReleaseRecord, error) {
	records := make(map[string]router.ReleaseRecord, len(apps))
	for _, app := range apps {
		release, promoted := promotion.Releases[app]
		if !promoted {
			continue
		}
		record, staged, err := l.Record(ctx, app, release)
		if err != nil {
			return nil, err
		}
		if !staged {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"promotion %s names release %s of %s, and this project's ledger staged no record for it, so nothing says what that release is. Re-run the deploy that staged it",
				promotion.PromotionID, release, app)
		}
		if record.Secrets, err = l.openSecrets(ctx, record); err != nil {
			return nil, err
		}
		records[app] = record
	}
	return records, nil
}

func (l projectLedger) openSecrets(ctx context.Context, record router.ReleaseRecord) (router.ReleaseSecrets, error) {
	if len(record.Sealed) == 0 {
		return router.ReleaseSecrets{}, nil
	}
	opened, err := l.cipher.Open(ctx, l.tier, l.newSecretsAssociatedData(record.App, record.Release), record.Sealed)
	if err != nil {
		return router.ReleaseSecrets{}, fmt.Errorf("open the secrets the ledger keeps for %s/%s: %w", record.App, record.Release, err)
	}
	var secrets router.ReleaseSecrets
	if err := json.Unmarshal(opened, &secrets); err != nil {
		return router.ReleaseSecrets{}, fmt.Errorf("read the secrets the ledger keeps for %s/%s: %w", record.App, record.Release, err)
	}
	return secrets, nil
}

const releaseSecretsPurpose = "release secrets"

func ownerOfReleaseSecrets(bound seal.AssociatedData) (records.Owner, bool) {
	project, named := bound.Project()
	if bound.Purpose() == releaseSecretsPurpose && named && len(bound.Values()) == 2 {
		return records.Owner{Project: project, EnvironmentInValue: true}, true
	}
	return records.Owner{}, false
}
