package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/networkconnectivity/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type Patches struct {
	transform.Result
	project  string
	recorded recordedPatches
}

type recordedPatches struct {
	Patches transform.Patches           `json:"patches,omitempty"`
	Tags    map[string]string           `json:"tags,omitempty"`
	Project string                      `json:"project,omitempty"`
	Applied map[string]surface.Recorded `json:"applied,omitempty"`
}

func (c recordedPatches) result() transform.Result {
	return transform.Result{Patches: c.Patches, Tags: c.Tags}
}

func isEmpty(result transform.Result) bool {
	return !slices.ContainsFunc(slices.Collect(maps.Values(result.Patches)), func(patch map[string]any) bool { return len(patch) > 0 }) && len(result.Tags) == 0
}

func digestOf(result transform.Result) string {
	if isEmpty(result) {
		return ""
	}
	encoded, err := json.Marshal(struct {
		Patches transform.Patches `json:"patches,omitempty"`
		Tags    map[string]string `json:"tags,omitempty"`
	}{result.Patches, result.Tags})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (p Patches) Digest() string { return digestOf(p.Result) }

func (p Patches) isChanged() bool { return p.Digest() != digestOf(p.recorded.result()) }

func patchesKey(tier environment.Tier) records.Key {
	return records.Partition{Tier: tier, Root: surface.RootPatches}.Key(TransformKind)
}

func ReadPatches(ctx context.Context, b bootstrap.Engine, tier environment.Tier, project string, evaluated transform.Result) (Patches, error) {
	recorded, err := readRecorded(ctx, b.Records, tier)
	if err != nil {
		return Patches{}, err
	}
	patches := Patches{Result: evaluated, project: project, recorded: recorded}
	if isEmpty(evaluated) && (project == "" || project != recorded.Project) {
		patches.Result, patches.project = recorded.result(), recorded.Project
	}
	if _, err := patches.patch(b, tier); err != nil {
		return Patches{}, err
	}
	return patches, nil
}

func readRecorded(ctx context.Context, store records.Store, tier environment.Tier) (recordedPatches, error) {
	if store == nil {
		return recordedPatches{}, nil
	}
	entry, err := records.ReadOrEmpty(ctx, store, patchesKey(tier))
	if errors.Is(err, ports.ErrNoRecordsDatabase) {
		return recordedPatches{}, nil
	}
	if err != nil {
		return recordedPatches{}, fmt.Errorf("read the transforms the %s tier's network records: %w", tier, err)
	}
	var recorded recordedPatches
	if len(entry.Value) == 0 {
		return recorded, nil
	}
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return recordedPatches{}, fmt.Errorf("read the transforms the %s tier's network records: %w", tier, err)
	}
	return recorded, nil
}

func (p *Patches) record(ctx context.Context, store records.Store, tier environment.Tier, applied map[string]surface.Recorded) error {
	next := recordedPatches{Patches: p.Patches, Tags: p.Tags, Project: p.project, Applied: applied}
	if isEmpty(p.Result) {
		next.Patches, next.Tags, next.Project = nil, nil, ""
	}
	value, err := json.Marshal(next)
	if err != nil {
		return err
	}
	prior, err := json.Marshal(p.recorded)
	if err != nil || bytes.Equal(value, prior) {
		return err
	}
	key := patchesKey(tier)
	if isEmpty(next.result()) && len(next.Applied) == 0 {
		err = records.Forget(ctx, store, key)
	} else {
		err = records.Change(ctx, store, key, func(entry records.Entry) ([]byte, bool, error) {
			return value, !bytes.Equal(entry.Value, value), nil
		})
	}
	if err != nil {
		return fmt.Errorf("record the transforms the %s tier's network records: %w", tier, err)
	}
	p.recorded = next
	return nil
}

func forgetPatches(ctx context.Context, store records.Store, tier environment.Tier) error {
	if store == nil {
		return nil
	}
	err := records.Forget(ctx, store, patchesKey(tier))
	if err != nil && !errors.Is(err, ports.ErrNoRecordsDatabase) {
		return fmt.Errorf("forget the transforms recorded for the %s tier's network: %w", tier, err)
	}
	return nil
}

type patchedNetwork struct {
	network    surface.Patched[compute.Network, compute.Network]
	subnetwork surface.Patched[compute.Subnetwork, subnetworkUpdate]
	policies   []surface.Patched[networkconnectivity.ServiceConnectionPolicy, policyUpdate]
}

func (p Patches) patch(b bootstrap.Engine, tier environment.Tier) (patchedNetwork, error) {
	named := "the " + string(tier) + " tier's network"
	var patched patchedNetwork
	var err error
	if patched.network, err = networkSurface.Patch(named, desiredNetwork(b, tier), p.Patches[networkSurfaceName], nil, p.recorded.Applied[networkSurfaceName]); err != nil {
		return patchedNetwork{}, err
	}
	if patched.subnetwork, err = subnetworkSurface.Patch(named, desiredSubnetwork(b, tier), p.Patches[subnetworkSurfaceName], nil, p.recorded.Applied[subnetworkSurfaceName]); err != nil {
		return patchedNetwork{}, err
	}
	for _, policy := range connectionPolicies {
		each, err := policySurface.Patch(named, desiredPolicy(b, tier, policy), p.Patches[policySurfaceName], p.Tags, p.recorded.Applied[policySurfaceName])
		if err != nil {
			return patchedNetwork{}, err
		}
		patched.policies = append(patched.policies, each)
	}
	return patched, nil
}

func (p Patches) subnetRange(b bootstrap.Engine, tier environment.Tier) string {
	patched, err := p.patch(b, tier)
	if err != nil || patched.subnetwork.Desired.IpCidrRange == "" {
		return SubnetRange
	}
	return patched.subnetwork.Desired.IpCidrRange
}

func (p patchedNetwork) applied() map[string]surface.Recorded {
	applied := map[string]surface.Recorded{}
	for name, recorded := range map[string]surface.Recorded{
		networkSurfaceName:    p.network.Recorded(),
		subnetworkSurfaceName: p.subnetwork.Recorded(),
		policySurfaceName:     p.policies[0].Recorded(),
	} {
		if !recorded.IsEmpty() {
			applied[name] = recorded
		}
	}
	if len(applied) == 0 {
		return nil
	}
	return applied
}

func joinApplied(before, after map[string]surface.Recorded) map[string]surface.Recorded {
	if len(before) == 0 {
		return after
	}
	joined := maps.Clone(before)
	for name, recorded := range after {
		joined[name] = joined[name].With(recorded)
	}
	return joined
}
