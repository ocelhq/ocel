package providerserver

import (
	"context"
	"errors"
	"reflect"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func (r *deployRun) enforcePreviewProtection(ctx context.Context, progress progress.Log) error {
	if r.spec.Tier != environment.TierPreview {
		return nil
	}
	records, err := stackrecords.ReadPreviewGates(ctx, r.provider.KeyValues(), r.provider.Cipher())
	if err != nil {
		return err
	}
	record, recorded := records[r.spec.Slug]
	if !r.protectPreviews {
		if recorded && record.Enabled {
			progress.Warn("previews.protection is off for " + r.spec.Slug + ", so this deploy opens its previews to anyone who has their URLs. " +
				"Their password and bypass secrets are kept: turn previews.protection back on and deploy a preview to protect them with the same credentials")
		}
		return nil
	}
	if !r.front.Facts().GatesPreviews {
		return refusal.Refuse(refusal.CodeUnsupported,
			"previews.protection is on for %s, and the %s edge does not run the preview gate, so it would serve this preview to anyone: deploy the preview through an edge that gates previews, or turn previews.protection off",
			r.spec.Slug, r.front.Kind())
	}
	if r.dry {
		if recorded {
			r.previewGate = &record.Gate
		}
		return nil
	}
	var enforced stackrecords.PreviewGateRecord
	err = stackrecords.ChangePreviewGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), func(records map[string]stackrecords.PreviewGateRecord) error {
		record, recorded := records[r.spec.Slug]
		switch {
		case !recorded:
			created, err := newPreviewGateRecord()
			if err != nil {
				return err
			}
			record = created
		case !record.Enabled:
			record.Enabled, record.Pending = true, true
		}
		records[r.spec.Slug] = record
		enforced = record
		return nil
	})
	if err != nil {
		return err
	}
	r.previewGate = &enforced.Gate
	return nil
}

func (r *deployRun) readPreviewGateInForce(ctx context.Context) error {
	if r.spec.Tier != environment.TierPreview || r.dry || !r.protectPreviews {
		return nil
	}
	gate, err := stackrecords.ReadPreviewGate(ctx, r.provider.KeyValues(), r.provider.Cipher(), r.spec.Slug)
	if err != nil {
		return err
	}
	r.previewGate = gate
	return nil
}

func (r *deployRun) settlePreviewGate(ctx context.Context, progress progress.Log) error {
	if r.spec.Tier != environment.TierPreview || r.dry {
		return nil
	}
	if !r.protectPreviews {
		if err := r.disablePreviewGate(ctx); err != nil {
			return err
		}
	}
	publish := previewGatePublisher(ctx, r.front, r.edgeStack().State(), progress)
	err := settlePreviewGate(ctx, r.provider, r.spec.Slug, r.previewGate, publish)
	if errors.Is(err, errPreviewGateUnpublished) {
		note := "The preview credentials of " + r.spec.Slug + " changed while this deploy wrote them to " + describeFront(r.front.Kind()) +
			", and it may still hold the older ones: run `ocel preview protection sync` to publish the recorded credentials"
		progress.Warn(note)
		r.pending = append(r.pending, note)
		return nil
	}
	return err
}

func (r *deployRun) disablePreviewGate(ctx context.Context) error {
	return stackrecords.ChangePreviewGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), func(records map[string]stackrecords.PreviewGateRecord) error {
		record, recorded := records[r.spec.Slug]
		if !recorded || !record.Enabled {
			return nil
		}
		record.Enabled, record.Pending = false, false
		records[r.spec.Slug] = record
		return nil
	})
}

func samePreviewGate(a, b *previewgate.Gate) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(normalisedGate(*a), normalisedGate(*b))
}

func normalisedGate(gate previewgate.Gate) previewgate.Gate {
	if gate.BypassSecrets == nil {
		gate.BypassSecrets = []string{}
	}
	if gate.AllowOptions == nil {
		gate.AllowOptions = []string{}
	}
	return gate
}
