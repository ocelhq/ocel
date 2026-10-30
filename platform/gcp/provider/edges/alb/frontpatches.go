package alb

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/albresources"
)

const frontPatchesKey = "transforms"

func frontPatchesAt(tier environment.Tier) records.Key {
	return stackrecords.EdgeStacksPartition(tier).Key(string(albresources.Kind), frontPatchesKey)
}

type frontRecord struct {
	recordedPatches
	Project string `json:"project,omitempty"`
}

func (e *Edge) evaluateFront(ctx context.Context, tier environment.Tier) (recordedPatches, error) {
	result, err := e.deps.Engine.EvaluateFront(ctx, tier)
	return patchesOf(result), err
}

func (e *Edge) readRecordedFront(ctx context.Context, tier environment.Tier) (records.Entry, frontRecord, error) {
	entry, err := records.ReadOrEmpty(ctx, e.deps.KeyValues, frontPatchesAt(tier))
	if err != nil {
		return records.Entry{}, frontRecord{}, fmt.Errorf("read the transforms recorded for the %s load balancer of tier %s: %w", albresources.Kind, tier, err)
	}
	var recorded frontRecord
	if len(entry.Value) > 0 {
		if err := json.Unmarshal(entry.Value, &recorded); err != nil {
			return records.Entry{}, frontRecord{}, fmt.Errorf("decode the transforms recorded for the %s load balancer of tier %s: %w", albresources.Kind, tier, err)
		}
	}
	return entry, recorded, nil
}

func (e *Edge) readFrontPatches(ctx context.Context, tier environment.Tier, project string) (frontRecord, frontRecord, error) {
	evaluated, err := e.evaluateFront(ctx, tier)
	if err != nil {
		return frontRecord{}, frontRecord{}, err
	}
	_, recorded, err := e.readRecordedFront(ctx, tier)
	if err != nil {
		return frontRecord{}, frontRecord{}, err
	}
	switch {
	case !evaluated.isEmpty():
		return frontRecord{recordedPatches: evaluated, Project: cmp.Or(project, recorded.Project)}, recorded, nil
	case project != "" && recorded.Project == project:
		return frontRecord{}, recorded, nil
	}
	return recorded, recorded, nil
}

func (e *Edge) recordFront(ctx context.Context, tier environment.Tier, applied frontRecord) error {
	entry, recorded, err := e.readRecordedFront(ctx, tier)
	if err != nil || applied.Project == recorded.Project && applied.equal(recorded.recordedPatches) {
		return err
	}
	if applied.isEmpty() {
		return records.Forget(ctx, e.deps.KeyValues, frontPatchesAt(tier))
	}
	encoded, err := json.Marshal(applied)
	if err != nil {
		return fmt.Errorf("encode the transforms recorded for the %s load balancer of tier %s: %w", albresources.Kind, tier, err)
	}
	entry.Value = encoded
	_, err = e.deps.KeyValues.Write(ctx, entry)
	if errors.Is(err, records.ErrStale) {
		if _, meanwhile, err := e.readRecordedFront(ctx, tier); err != nil || applied.equal(meanwhile.recordedPatches) {
			return err
		}
		return refusal.Refuse(refusal.CodeBusy,
			"another deploy patched the %s load balancer of tier %s while this one did: deploy again once it has finished", albresources.Kind, tier)
	}
	if err != nil {
		return fmt.Errorf("record the transforms recorded for the %s load balancer of tier %s: %w", albresources.Kind, tier, err)
	}
	return nil
}

func (e *Edge) forgetFront(ctx context.Context, tier environment.Tier) error {
	if err := records.Forget(ctx, e.deps.KeyValues, frontPatchesAt(tier)); err != nil {
		return fmt.Errorf("forget the transforms recorded for the %s load balancer of tier %s: %w", albresources.Kind, tier, err)
	}
	return nil
}

func (e *Edge) repatchFront(ctx context.Context, tier environment.Tier, project string, progress progress.Log) (LoadBalancer, bool, error) {
	wanted, recorded, err := e.readFrontPatches(ctx, tier, project)
	if err != nil {
		return LoadBalancer{}, false, err
	}
	if wanted.equal(recorded.recordedPatches) {
		return LoadBalancer{}, false, e.recordFront(ctx, tier, wanted)
	}
	progress.Say(fmt.Sprintf("Applying the transforms that patch the %s load balancer of the %s tier: they differ from the ones it was provisioned with", albresources.Kind, tier))
	balancer, err := e.raise(ctx, tier, project, progress)
	return balancer, true, err
}

func (e *Edge) PlanFrontRepatch(ctx context.Context, tier environment.Tier, project string) (*provider.ChangeGroup, error) {
	installed, err := e.bootstrapInstalled(ctx, tier)
	if err != nil || !installed {
		return nil, err
	}
	wanted, recorded, err := e.readFrontPatches(ctx, tier, project)
	if err != nil || wanted.equal(recorded.recordedPatches) {
		return nil, err
	}
	return &provider.ChangeGroup{
		Kind:   provider.StackGroupKind,
		Name:   albresources.LoadBalancerStack(tier),
		Action: provider.ActionUpdate,
		Reason: fmt.Sprintf("the transforms that patch the %s load balancer of the %s tier differ from the ones it records, so this deploy provisions it again with them, for every project in the tier", albresources.Kind, tier),
		Slow:   true,
	}, nil
}
