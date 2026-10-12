package providerserver

import (
	"cmp"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
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
	if r.front.Facts().GatesPreviewsAtOrigin && !r.provider.Facts().RunsPreviewGates {
		return refusal.Refuse(refusal.CodeUnsupported,
			"previews.protection is on for %s, and the %s edge leaves the preview gate to the origin, whose services run none, so it would serve this preview to anyone: deploy the preview through an edge that gates previews itself, or turn previews.protection off",
			r.spec.Slug, r.front.Kind())
	}
	if err := r.warnPreviewGateIgnored(ctx, progress); err != nil {
		return err
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

func (r *deployRun) readPreviewGateInForce(ctx context.Context) (*previewgate.Gate, error) {
	if r.spec.Tier != environment.TierPreview || r.dry {
		return r.previewGate, nil
	}
	gate, err := stackrecords.ReadPreviewGate(ctx, r.provider.KeyValues(), r.provider.Cipher(), r.spec.Slug)
	if err != nil {
		return nil, err
	}
	if r.protectPreviews {
		r.previewGate = gate
	}
	return gate, nil
}

func (r *deployRun) settlePreviewGate(ctx context.Context, written *previewgate.Gate, progress progress.Log) error {
	if r.spec.Tier != environment.TierPreview || r.dry {
		return nil
	}
	publish := previewGatePublisher(ctx, r.front, r.edgeStack().State(), progress)
	err := settlePreviewGate(ctx, r.provider, r.spec.Slug, written, publish)
	if errors.Is(err, errPreviewGateUnpublished) {
		note := "The preview credentials of " + r.spec.Slug + " changed while this deploy wrote them to " + describeFront(r.front.Kind()) +
			", and it may still hold the older ones: run `ocel preview protection sync` to publish the recorded credentials"
		progress.Warn(note)
		r.pending = append(r.pending, note)
		return nil
	}
	return err
}

func (r *deployRun) disablePreviewGate(ctx context.Context) (bool, error) {
	if r.spec.Tier != environment.TierPreview || r.dry || r.protectPreviews {
		return false, nil
	}
	disabled := false
	err := stackrecords.ChangePreviewGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), func(records map[string]stackrecords.PreviewGateRecord) error {
		record, recorded := records[r.spec.Slug]
		disabled = recorded && record.Enabled
		if !disabled {
			return nil
		}
		record.Enabled, record.Pending = false, false
		records[r.spec.Slug] = record
		return nil
	})
	return disabled, err
}

func (r *deployRun) restorePreviewGate(ctx context.Context) error {
	return stackrecords.ChangePreviewGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), func(records map[string]stackrecords.PreviewGateRecord) error {
		record, recorded := records[r.spec.Slug]
		if !recorded || record.Enabled {
			return nil
		}
		record.Enabled, record.Pending = true, true
		records[r.spec.Slug] = record
		return nil
	})
}

func (r *deployRun) appPreviewGate() *previewgate.Gate {
	if !r.front.Facts().GatesPreviewsAtOrigin {
		return nil
	}
	return r.previewGate
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

func (r *deployRun) warnPreviewGateIgnored(ctx context.Context, progress progress.Log) error {
	if !r.front.Facts().GatesPreviewsAtOrigin {
		return nil
	}
	ignoring, err := listPreviewFunctionsIgnoringGate(ctx, r.provider.KeyValues(), r.spec.Slug)
	if err != nil {
		return err
	}
	others := slices.DeleteFunc(ignoring, func(function previewFunction) bool { return function.Preview == r.spec.Env })
	if len(others) == 0 {
		return nil
	}
	r.notePending(progress, "previews.protection is on for "+r.spec.Slug+", and "+describePreviewGateIgnored(others))
	return nil
}

func (r *deployRun) warnPreviewGateTurnedOn(ctx context.Context, functions []provider.Function, progress progress.Log) error {
	if r.spec.Tier != environment.TierPreview || r.dry || r.protectPreviews || !r.front.Facts().GatesPreviewsAtOrigin {
		return nil
	}
	var ignoring []previewFunction
	for _, function := range functions {
		if function.IgnoresPreviewGate {
			ignoring = append(ignoring, previewFunction{Preview: r.spec.Env, Function: function.Name})
		}
	}
	if len(ignoring) == 0 {
		return nil
	}
	records, err := stackrecords.ReadPreviewGates(ctx, r.provider.KeyValues(), r.provider.Cipher())
	if err != nil || !records[r.spec.Slug].Enabled {
		return err
	}
	r.notePending(progress, "previews.protection was turned on for "+r.spec.Slug+" while this deploy ran with it off, and "+describePreviewGateIgnored(ignoring))
	return nil
}

func (r *deployRun) notePending(progress progress.Log, note string) {
	progress.Warn(note)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending = append(r.pending, note)
}

type previewFunction struct {
	Preview  string
	Function string
}

func listPreviewFunctionsIgnoringGate(ctx context.Context, store keyvalue.Store, slug string) ([]previewFunction, error) {
	stacks, err := stackrecords.List(ctx, store, environment.TierPreview, slug)
	if err != nil {
		return nil, err
	}
	var ignoring []previewFunction
	for _, stack := range stacks {
		for _, function := range stack.Functions {
			named := previewFunction{Preview: stack.Name.Env, Function: function.Name}
			if function.IgnoresPreviewGate && !slices.Contains(ignoring, named) {
				ignoring = append(ignoring, named)
			}
		}
	}
	slices.SortFunc(ignoring, func(a, b previewFunction) int {
		return cmp.Or(cmp.Compare(a.Preview, b.Preview), cmp.Compare(a.Function, b.Function))
	})
	return ignoring, nil
}

func describePreviewGateIgnored(ignoring []previewFunction) string {
	named := make([]string, 0, len(ignoring))
	for _, function := range ignoring {
		named = append(named, "function "+function.Function+" of preview "+function.Preview)
	}
	return "these previews answer anyone who has their URLs, because their functions ignore the preview gate: " + strings.Join(named, ", ") +
		". Destroy those previews, or deploy them again with functions that check the gate"
}
