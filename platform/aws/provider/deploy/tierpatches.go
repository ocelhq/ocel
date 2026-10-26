package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

const (
	transformTypeNetwork = "network"
	vpcSurface           = "vpc"
	vpcBlockField        = "cidrBlock"
)

type tierPatches struct {
	network         network.Patches
	functionNetwork component.Patches
}

func (r *Stacks) tierPatchesFor(ctx context.Context, tier environment.Tier) (tierPatches, error) {
	r.tierMu.Lock()
	defer r.tierMu.Unlock()
	if evaluated, done := r.tierEvaluated[tier]; done {
		return evaluated, nil
	}
	opened, err := r.at(ctx, network.StackRef(tier), "")
	if err != nil {
		return tierPatches{}, err
	}
	evaluated, err := evaluateTierPatches(ctx, opened.cfg.Transform, opened.cfg.Namespace, tier)
	if err != nil {
		return tierPatches{}, err
	}
	r.tierEvaluated[tier] = evaluated
	return evaluated, nil
}

func (r *Stacks) networkPatches(ctx context.Context, tier environment.Tier) (network.Patches, error) {
	evaluated, err := r.tierPatchesFor(ctx, tier)
	return evaluated.network, err
}

func (r *Stacks) functionNetworkPatches(ctx context.Context, tier environment.Tier) (component.Patches, error) {
	evaluated, err := r.tierPatchesFor(ctx, tier)
	return evaluated.functionNetwork, err
}

func evaluateTierPatches(ctx context.Context, pass transform.Pass, namespace string, tier environment.Tier) (tierPatches, error) {
	if pass == nil {
		return tierPatches{}, nil
	}
	key := resourceKey{Type: transformTypeNetwork, Name: string(tier)}
	results, err := pass.Evaluate(ctx, transform.Request{
		Provider:  transformProvider,
		EnvTier:   string(tier),
		Resources: []transform.Resource{{Type: key.Type, Name: key.Name}},
	})
	if err != nil || results == nil {
		return tierPatches{}, err
	}
	if len(results) != 1 {
		return tierPatches{}, fmt.Errorf("transforms returned %d results for the %s tier's network", len(results), tier)
	}
	if err := walkOutputs([]transformCandidate{{key: key}}, results, func(ref outputRef, at outputSite, _ any) (any, error) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"a transform fills the %s tier's network's %s.%s from %s, and the network belongs to every project of the tier, so it reads no project's records",
			tier, at.Surface, at.Field, ref)
	}); err != nil {
		return tierPatches{}, err
	}
	result, cidr, err := takeNetworkBlock(tier, results[0])
	if err != nil {
		return tierPatches{}, err
	}
	parts := map[string]map[string][]component.ResourceRef{
		"network":          network.ResourceNames(namespace, tier),
		"function network": functionnetwork.ResourceNames(namespace, tier),
	}
	for _, patched := range slices.Sorted(maps.Keys(result.Patches)) {
		if _, constructed := parts["network"][patched]; constructed {
			continue
		}
		if _, constructed := parts["function network"][patched]; constructed {
			continue
		}
		return tierPatches{}, refusal.Refuse(refusal.CodeInvalid,
			"a transform patches the %s tier's network's %s, and ocel constructs no such resource for it", tier, patched)
	}
	evaluated := tierPatches{network: network.Patches{CIDR: cidr}}
	if evaluated.network.Patches, err = patchesOver(key, parts["network"], result); err != nil {
		return tierPatches{}, err
	}
	if evaluated.functionNetwork, err = patchesOver(key, parts["function network"], result); err != nil {
		return tierPatches{}, err
	}
	return evaluated, nil
}

func patchesOver(key resourceKey, names map[string][]component.ResourceRef, result transform.Result) (component.Patches, error) {
	subset := transform.Result{Patches: transform.Patches{}, Tags: result.Tags}
	for patched, patch := range result.Patches {
		if _, constructed := names[patched]; constructed && len(patch) > 0 {
			subset.Patches[patched] = patch
		}
	}
	if len(subset.Patches) == 0 && len(subset.Tags) == 0 {
		return component.Patches{}, nil
	}
	indexed, err := indexPatches([]transformCandidate{{key: key, names: names}}, []transform.Result{subset})
	if err != nil {
		return component.Patches{}, err
	}
	encoded, err := json.Marshal(subset)
	if err != nil {
		return component.Patches{}, fmt.Errorf("encode the patches of the %s tier's network: %w", key.Name, err)
	}
	sum := sha256.Sum256(encoded)
	return component.Patches{Digest: hex.EncodeToString(sum[:]), Tags: subset.Tags, Install: indexed.install}, nil
}

func takeNetworkBlock(tier environment.Tier, result transform.Result) (transform.Result, string, error) {
	vpc := result.Patches[vpcSurface]
	block, set := vpc[vpcBlockField]
	if !set {
		return result, "", nil
	}
	cidr, isString := block.(string)
	if !isString {
		return transform.Result{}, "", refusal.Refuse(refusal.CodeInvalid,
			"a transform sets the %s tier's network's %s.%s to %v, and it takes a CIDR block such as 10.64.0.0/16",
			tier, vpcSurface, vpcBlockField, block)
	}
	if _, err := network.NewLayout(cidr); err != nil {
		return transform.Result{}, "", refusal.Refuse(refusal.CodeInvalid,
			"a transform sets the %s tier's network's %s.%s: %v", tier, vpcSurface, vpcBlockField, err)
	}
	rest := maps.Clone(result.Patches)
	rest[vpcSurface] = maps.Clone(vpc)
	delete(rest[vpcSurface], vpcBlockField)
	return transform.Result{Patches: rest, Tags: result.Tags}, cidr, nil
}
