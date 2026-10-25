package providerserver

import (
	"cmp"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func protectionKey(tier environment.Tier) string {
	return "protection." + string(tier)
}

func describeProtected(tier environment.Tier, slug string) string {
	if tier == environment.TierPreview {
		return "the previews of " + slug
	}
	return "the " + string(tier) + " of " + slug
}

func (r *deployRun) enforceProtection(ctx context.Context, progress progress.Log) error {
	tier := r.spec.Tier
	records, err := stackrecords.ReadGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), tier)
	if err != nil {
		return err
	}
	record, recorded := records[r.spec.Slug]
	if !r.protect {
		if recorded && record.Enabled {
			progress.Warn(protectionKey(tier) + " is off, so this deploy opens " + describeProtected(tier, r.spec.Slug) + " to anyone with their URLs; its credentials are kept for when it is back on")
		}
		return nil
	}
	if !r.front.Facts().Gates {
		return refusal.Refuse(refusal.CodeUnsupported,
			"%s is on for %s, and the %s edge does not run the gate, so it would serve %s to anyone: deploy through an edge that gates, or turn %s off",
			protectionKey(tier), r.spec.Slug, r.front.Kind(), describeProtected(tier, r.spec.Slug), protectionKey(tier))
	}
	if r.front.Facts().GatesAtOrigin && !r.provider.Facts().RunsGates {
		return refusal.Refuse(refusal.CodeUnsupported,
			"%s is on for %s, and the %s edge leaves the gate to the origin, whose services run none, so it would serve %s to anyone: deploy through an edge that gates itself, or turn %s off",
			protectionKey(tier), r.spec.Slug, r.front.Kind(), describeProtected(tier, r.spec.Slug), protectionKey(tier))
	}
	if err := r.warnGateIgnored(ctx, progress); err != nil {
		return err
	}
	return r.mode.protection(ctx, r, gateReading{record: record, recorded: recorded})
}

type gateReading struct {
	record   stackrecords.GateRecord
	recorded bool
}

func (r *deployRun) enforceGate(ctx context.Context) error {
	tier := r.spec.Tier
	var enforced stackrecords.GateRecord
	err := stackrecords.ChangeGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), tier, func(records map[string]stackrecords.GateRecord) error {
		record, recorded := records[r.spec.Slug]
		record, err := gateToEnforce(gateReading{record: record, recorded: recorded})
		if err != nil {
			return err
		}
		records[r.spec.Slug] = record
		enforced = record
		return nil
	})
	if err != nil {
		return err
	}
	r.protection = &enforced.Gate
	return nil
}

func gateToEnforce(gates gateReading) (stackrecords.GateRecord, error) {
	record := gates.record
	switch {
	case !gates.recorded:
		return newGateRecord()
	case !record.Enabled:
		record.Enabled, record.Pending = true, true
	}
	return record, nil
}

func (r *deployRun) readGateInForce(ctx context.Context) (*gate.Gate, string, error) {
	records, err := stackrecords.ReadGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), r.spec.Tier)
	if err != nil {
		return nil, "", err
	}
	record := records[r.spec.Slug]
	held := record.GateInForce()
	if r.protect {
		r.protection = held
	}
	return held, record.Password, nil
}

func gateForEdge(front edge.Edge, held *gate.Gate, password string) *gate.Gate {
	if held == nil || held.PasswordHash == "" || !front.Facts().ChecksSignedPasswords {
		return held
	}
	signed := *held
	signed.PasswordHash = gate.SignPassword(held.Key, password)
	return &signed
}

func (r *deployRun) settleGate(ctx context.Context, written *gate.Gate, progress progress.Log) error {
	publish := gatePublisher(ctx, r.front, r.edgeStack().State(), progress)
	err := settleGate(ctx, r.provider, r.spec.Tier, r.spec.Slug, written, publish)
	if errors.Is(err, errGateUnpublished) {
		note := "The " + string(r.spec.Tier) + " credentials of " + r.spec.Slug + " changed while this deploy wrote them to " + describeFront(r.front.Kind()) +
			", and it may still hold the older ones: run `ocel protection sync" + tierFlag(r.spec.Tier) + "` to publish the recorded credentials"
		progress.Warn(note)
		r.pending = append(r.pending, note)
		return nil
	}
	return err
}

func tierFlag(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return " --preview"
	}
	return ""
}

func (r *deployRun) disableGate(ctx context.Context) (bool, error) {
	if r.protect {
		return false, nil
	}
	disabled := false
	err := stackrecords.ChangeGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), r.spec.Tier, func(records map[string]stackrecords.GateRecord) error {
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

func (r *deployRun) restoreGate(ctx context.Context) error {
	return stackrecords.ChangeGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), r.spec.Tier, func(records map[string]stackrecords.GateRecord) error {
		record, recorded := records[r.spec.Slug]
		if !recorded || record.Enabled {
			return nil
		}
		record.Enabled, record.Pending = true, true
		records[r.spec.Slug] = record
		return nil
	})
}

func (r *deployRun) appGate() *gate.Gate {
	if !r.front.Facts().GatesAtOrigin {
		return nil
	}
	return r.protection
}

func sameGate(a, b *gate.Gate) bool {
	if a == nil || b == nil {
		return a == b
	}
	return reflect.DeepEqual(normalisedGate(*a), normalisedGate(*b))
}

func normalisedGate(held gate.Gate) gate.Gate {
	if held.BypassSecrets == nil {
		held.BypassSecrets = []string{}
	}
	if held.AllowOptions == nil {
		held.AllowOptions = []string{}
	}
	return held
}

func (r *deployRun) warnGateIgnored(ctx context.Context, progress progress.Log) error {
	if !r.front.Facts().GatesAtOrigin {
		return nil
	}
	tier := r.spec.Tier
	ignoring, err := listFunctionsIgnoringGate(ctx, r.provider.KeyValues(), tier, r.spec.Slug)
	if err != nil {
		return err
	}
	others := slices.DeleteFunc(ignoring, func(function gatedFunction) bool { return function.Env == r.spec.Env })
	if len(others) == 0 {
		return nil
	}
	r.notePending(progress, protectionKey(tier)+" is on for "+r.spec.Slug+", and "+describeGateIgnored(tier, others))
	return nil
}

func (r *deployRun) gateTurnedOn(ctx context.Context, functions []provider.Function) (string, error) {
	if r.protect || !r.front.Facts().GatesAtOrigin {
		return "", nil
	}
	tier := r.spec.Tier
	var ignoring []gatedFunction
	for _, function := range functions {
		if function.IgnoresGate {
			ignoring = append(ignoring, gatedFunction{Env: r.spec.Env, Function: function.Name})
		}
	}
	if len(ignoring) == 0 {
		return "", nil
	}
	records, err := stackrecords.ReadGates(ctx, r.provider.KeyValues(), r.provider.Cipher(), tier)
	if err != nil || !records[r.spec.Slug].Enabled {
		return "", err
	}
	return protectionKey(tier) + " was turned on for " + r.spec.Slug + " while this deploy ran with it off, and " + describeGateIgnored(tier, ignoring), nil
}

func (r *deployRun) notePending(progress progress.Log, note string) {
	progress.Warn(note)
	r.pending = append(r.pending, note)
}

type gatedFunction struct {
	Env      string
	Function string
}

func listFunctionsIgnoringGate(ctx context.Context, store records.Store, tier environment.Tier, slug string) ([]gatedFunction, error) {
	stacks, err := stackrecords.List(ctx, store, tier, slug)
	if err != nil {
		return nil, err
	}
	var ignoring []gatedFunction
	for _, stack := range stacks {
		for _, function := range stack.Functions {
			named := gatedFunction{Env: stack.Name.Env, Function: function.Name}
			if function.IgnoresGate && !slices.Contains(ignoring, named) {
				ignoring = append(ignoring, named)
			}
		}
	}
	slices.SortFunc(ignoring, func(a, b gatedFunction) int {
		return cmp.Or(cmp.Compare(a.Env, b.Env), cmp.Compare(a.Function, b.Function))
	})
	return ignoring, nil
}

func describeGateIgnored(tier environment.Tier, ignoring []gatedFunction) string {
	named := make([]string, 0, len(ignoring))
	for _, function := range ignoring {
		if tier == environment.TierPreview {
			named = append(named, "function "+function.Function+" of preview "+function.Env)
			continue
		}
		named = append(named, "function "+function.Function+" of "+function.Env)
	}
	return "these " + string(tier) + " functions answer anyone who has their URLs, because they ignore the gate: " + strings.Join(named, ", ") +
		". Destroy them, or deploy again with functions that check the gate"
}
