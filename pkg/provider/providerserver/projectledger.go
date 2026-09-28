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

func (l projectLedger) envelopeBound(app, build string) seal.AssociatedData {
	return seal.AssociatedData{
		{Name: "project", Value: l.slug},
		{Name: "app", Value: app},
		{Name: "build", Value: build},
		{Name: "field", Value: "envelope"},
	}
}

func (l projectLedger) putStaged(ctx context.Context, record router.DeploymentRecord) error {
	if record.Envelope != "" {
		sealed, err := l.cipher.Seal(ctx, l.tier, l.envelopeBound(record.App, record.Build), []byte(record.Envelope))
		if err != nil {
			return fmt.Errorf("seal the envelope %s/%s serves with before the ledger keeps it: %w", record.App, record.Build, err)
		}
		record.Envelope = base64.StdEncoding.EncodeToString(sealed)
	}
	return l.PutStaged(ctx, record)
}

func (l projectLedger) records(ctx context.Context, promotion router.Promotion, apps []string) (map[string]router.DeploymentRecord, error) {
	records := make(map[string]router.DeploymentRecord, len(apps))
	for _, app := range apps {
		build, promoted := promotion.Builds[app]
		if !promoted {
			continue
		}
		record, staged, err := l.Record(ctx, app, build)
		if err != nil {
			return nil, err
		}
		if !staged {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"promotion %s names build %s of %s, and this project's ledger staged no record for it, so nothing says what that build released. Re-run the deploy that built it",
				promotion.PromotionID, build, app)
		}
		if record.Envelope, err = l.openEnvelope(ctx, record); err != nil {
			return nil, err
		}
		records[app] = record
	}
	return records, nil
}

func (l projectLedger) openEnvelope(ctx context.Context, record router.DeploymentRecord) (string, error) {
	if record.Envelope == "" {
		return "", nil
	}
	sealed, err := base64.StdEncoding.DecodeString(record.Envelope)
	if err != nil {
		return "", fmt.Errorf("read the envelope the ledger keeps for %s/%s: %w", record.App, record.Build, err)
	}
	opened, err := l.cipher.Open(ctx, l.tier, l.envelopeBound(record.App, record.Build), sealed)
	if err != nil {
		return "", fmt.Errorf("open the envelope the ledger keeps for %s/%s: %w", record.App, record.Build, err)
	}
	return string(opened), nil
}

func (l projectLedger) active(ctx context.Context, pointer string) (router.Promotion, bool, error) {
	history, err := l.History(ctx, pointer)
	if err != nil {
		return router.Promotion{}, false, err
	}
	for _, entry := range history {
		if entry.Active {
			return entry.Promotion, true, nil
		}
	}
	return router.Promotion{}, false, nil
}
