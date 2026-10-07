package providerserver

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
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

func (l projectLedger) newEnvelopeAssociatedData(app, release string) seal.AssociatedData {
	return seal.AssociatedData{
		{Name: "project", Value: l.slug},
		{Name: "app", Value: app},
		{Name: "release", Value: release},
		{Name: "field", Value: "envelope"},
	}
}

func (l projectLedger) putStaged(ctx context.Context, record router.ReleaseRecord) error {
	if record.Envelope != "" {
		sealed, err := l.cipher.Seal(ctx, l.tier, l.newEnvelopeAssociatedData(record.App, record.Release), []byte(record.Envelope))
		if err != nil {
			return fmt.Errorf("seal the envelope %s/%s serves with before the ledger keeps it: %w", record.App, record.Release, err)
		}
		record.Envelope = base64.StdEncoding.EncodeToString(sealed)
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
		if record.Envelope, err = l.openEnvelope(ctx, record); err != nil {
			return nil, err
		}
		records[app] = record
	}
	return records, nil
}

func (l projectLedger) openEnvelope(ctx context.Context, record router.ReleaseRecord) (string, error) {
	if record.Envelope == "" {
		return "", nil
	}
	sealed, err := base64.StdEncoding.DecodeString(record.Envelope)
	if err != nil {
		return "", fmt.Errorf("read the envelope the ledger keeps for %s/%s: %w", record.App, record.Release, err)
	}
	opened, err := l.cipher.Open(ctx, l.tier, l.newEnvelopeAssociatedData(record.App, record.Release), sealed)
	if err != nil {
		return "", fmt.Errorf("open the envelope the ledger keeps for %s/%s: %w", record.App, record.Release, err)
	}
	return string(opened), nil
}
