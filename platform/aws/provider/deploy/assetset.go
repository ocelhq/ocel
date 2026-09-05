package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"
	"sync"

	"github.com/pulumi/pulumi-go-provider/infer"
	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
)

const (
	assetSetPackage = "ocel"
	assetSetVersion = "1.0.0"
	assetSetToken   = assetSetPackage + ":index:AssetSet"
)

const (
	staticAssetSetName    = "static-assets"
	prerenderAssetSetName = "prerender-assets"
	edgeBundleSetName     = "edge-bundle"
)

type assetSet struct {
	name   string
	app    string
	files  int
	digest string
	push   func(ctx context.Context, report providerkit.Reporter) error
}

type setManifest struct {
	h     hash.Hash
	files int
}

func newSetManifest() *setManifest { return &setManifest{h: sha256.New()} }

func (m *setManifest) add(bucket, key string, size int64) {
	writeLenPrefixed(m.h, []byte(bucket))
	writeLenPrefixed(m.h, []byte(key))
	writeLenPrefixed(m.h, []byte(strconv.FormatInt(size, 10)))
	m.files++
}

func (m *setManifest) digest() string { return hex.EncodeToString(m.h.Sum(nil)) }

type pendingSet struct {
	set    assetSet
	report providerkit.Reporter
}

type pendingSets struct {
	mu   sync.Mutex
	held map[string]pendingSet
}

func newPendingSets() *pendingSets { return &pendingSets{held: map[string]pendingSet{}} }

func (p *pendingSets) hold(stack string, sets []assetSet, report providerkit.Reporter) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, set := range sets {
		p.held[pendingKey(stack, set.name)] = pendingSet{set: set, report: report}
	}
}

func (p *pendingSets) drop(stack string, sets []assetSet) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, set := range sets {
		delete(p.held, pendingKey(stack, set.name))
	}
}

func (p *pendingSets) take(key string) (pendingSet, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	held, waiting := p.held[key]
	if waiting {
		delete(p.held, key)
	}
	return held, waiting
}

func pendingKey(stack, set string) string { return stack + "/" + set }

type assetSetArgs struct {
	Stack  string `pulumi:"stack"`
	App    string `pulumi:"app"`
	Set    string `pulumi:"set"`
	Files  int    `pulumi:"files"`
	Digest string `pulumi:"digest"`
}

type assetSetOutputs struct {
	assetSetArgs
	Pushed int `pulumi:"pushed"`
}

type assetSetResource struct {
	pending *pendingSets
}

func (*assetSetResource) Annotate(a infer.Annotator) { a.SetToken("index", "AssetSet") }

func (r *assetSetResource) Create(ctx context.Context, req infer.CreateRequest[assetSetArgs]) (infer.CreateResponse[assetSetOutputs], error) {
	id := pendingKey(req.Inputs.Stack, req.Inputs.Set)
	if req.DryRun {
		return infer.CreateResponse[assetSetOutputs]{ID: id, Output: assetSetOutputs{assetSetArgs: req.Inputs}}, nil
	}
	held, waiting := r.pending.take(id)
	if !waiting {
		return infer.CreateResponse[assetSetOutputs]{}, fmt.Errorf(
			"%s carries no %s to push, and this run would leave the plan's row unwritten", req.Inputs.Stack, req.Inputs.Set)
	}
	if err := held.set.push(ctx, held.report); err != nil {
		return infer.CreateResponse[assetSetOutputs]{}, err
	}
	return infer.CreateResponse[assetSetOutputs]{
		ID:     id,
		Output: assetSetOutputs{assetSetArgs: req.Inputs, Pushed: held.set.files},
	}, nil
}

func (r *release) assetSets(plan providerkit.StackPlan, app, runtime string, bundle appBundle, cache *isrConfig) ([]assetSet, edgeDelivery, error) {
	coord := appCoordinate(plan)
	var sets []assetSet
	for _, planned := range []func() (*assetSet, error){
		func() (*assetSet, error) { return staticAssetSet(r.cfg, app, runtime, coord) },
		func() (*assetSet, error) { return prerenderAssetSet(r.cfg, app, cache) },
	} {
		set, err := planned()
		if err != nil {
			return nil, edgeDelivery{}, err
		}
		if set != nil {
			sets = append(sets, *set)
		}
	}
	edgeSet, delivery, err := edgeBundleSet(r.cfg, app, coord, bundle)
	if err != nil {
		return nil, edgeDelivery{}, err
	}
	if edgeSet != nil {
		sets = append(sets, *edgeSet)
	}
	return sets, delivery, nil
}

func assetSetPlugin(pending *pendingSets) (kitpulumi.Plugin, error) {
	built, err := infer.NewProviderBuilder().
		WithNamespace(assetSetPackage).
		WithResources(infer.Resource(&assetSetResource{pending: pending})).
		Build()
	if err != nil {
		return kitpulumi.Plugin{}, fmt.Errorf("build the asset-set plugin the engine pushes uploads through: %w", err)
	}
	return kitpulumi.Plugin{Package: assetSetPackage, Version: assetSetVersion, Provider: built}, nil
}

type assetSetState struct {
	sdk.CustomResourceState
}

func declareAssetSets(pctx *sdk.Context, stack string, sets []assetSet) ([]sdk.Resource, error) {
	declared := make([]sdk.Resource, 0, len(sets))
	for _, set := range sets {
		state := &assetSetState{}
		err := pctx.RegisterResource(assetSetToken, set.name, sdk.Map{
			"stack":  sdk.String(stack),
			"app":    sdk.String(set.app),
			"set":    sdk.String(set.name),
			"files":  sdk.Int(set.files),
			"digest": sdk.String(set.digest),
		}, state)
		if err != nil {
			return nil, fmt.Errorf("declare %s's %s: %w", set.app, set.name, err)
		}
		declared = append(declared, state)
	}
	return declared, nil
}
