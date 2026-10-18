package site

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

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
)

const AssetSetToken = "ocel:index:AssetSet"

const (
	StaticAssetSetName    = "static-assets"
	PrerenderAssetSetName = "prerender-assets"
	EdgeBundleSetName     = "edge-bundle"
)

type AssetSet struct {
	name   string
	app    string
	files  int
	digest string
	push   func(ctx context.Context, progress progress.Log) error
}

type setManifest struct {
	h     hash.Hash
	files int
}

func newSetManifest() *setManifest { return &setManifest{h: sha256.New()} }

func (m *setManifest) add(bucket, key string, size int64) {
	provider.WriteLenPrefixed(m.h, []byte(bucket))
	provider.WriteLenPrefixed(m.h, []byte(key))
	provider.WriteLenPrefixed(m.h, []byte(strconv.FormatInt(size, 10)))
	m.files++
}

func (m *setManifest) digest() string { return hex.EncodeToString(m.h.Sum(nil)) }

type pendingSet struct {
	set      AssetSet
	progress progress.Log
}

type PendingSets struct {
	mu      sync.Mutex
	pending map[string]pendingSet
}

func NewPendingSets() *PendingSets { return &PendingSets{pending: map[string]pendingSet{}} }

func (p *PendingSets) Add(stack string, sets []AssetSet, progress progress.Log) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, set := range sets {
		p.pending[pendingKey(stack, set.name)] = pendingSet{set: set, progress: progress}
	}
}

func (p *PendingSets) Drop(stack string, sets []AssetSet) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, set := range sets {
		delete(p.pending, pendingKey(stack, set.name))
	}
}

func (p *PendingSets) take(key string) (pendingSet, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pending, waiting := p.pending[key]
	if waiting {
		delete(p.pending, key)
	}
	return pending, waiting
}

func (p *PendingSets) Push(ctx context.Context, stack, set string) (int, error) {
	pending, waiting := p.take(pendingKey(stack, set))
	if !waiting {
		return 0, fmt.Errorf(
			"%s has no %s waiting to push, and this run would leave the plan's row unwritten", stack, set)
	}
	if err := pending.set.push(ctx, pending.progress); err != nil {
		return 0, err
	}
	return pending.set.files, nil
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
	pending *PendingSets
}

func (*assetSetResource) Annotate(a infer.Annotator) { a.SetToken("index", "AssetSet") }

func (r *assetSetResource) Create(ctx context.Context, req infer.CreateRequest[assetSetArgs]) (infer.CreateResponse[assetSetOutputs], error) {
	id := pendingKey(req.Inputs.Stack, req.Inputs.Set)
	if req.DryRun {
		return infer.CreateResponse[assetSetOutputs]{ID: id, Output: assetSetOutputs{assetSetArgs: req.Inputs}}, nil
	}
	pushed, err := r.pending.Push(ctx, req.Inputs.Stack, req.Inputs.Set)
	if err != nil {
		return infer.CreateResponse[assetSetOutputs]{}, err
	}
	return infer.CreateResponse[assetSetOutputs]{
		ID:     id,
		Output: assetSetOutputs{assetSetArgs: req.Inputs, Pushed: pushed},
	}, nil
}

func ReadAssetSets(b Buckets, root string, coord naming.Coordinate, app *provider.AppSpec, bundle variables.Bundle, cache *ISR) ([]AssetSet, EdgeDelivery, error) {
	var sets []AssetSet
	for _, planned := range []func() (*AssetSet, error){
		func() (*AssetSet, error) { return staticAssetSet(b, root, app.App, app.Static, coord) },
		func() (*AssetSet, error) { return prerenderAssetSet(b, root, app.App, cache) },
	} {
		set, err := planned()
		if err != nil {
			return nil, EdgeDelivery{}, err
		}
		if set != nil {
			sets = append(sets, *set)
		}
	}
	edgeSet, delivery, err := edgeBundleSet(b, root, app.App, coord, bundle)
	if err != nil {
		return nil, EdgeDelivery{}, err
	}
	if edgeSet != nil {
		sets = append(sets, *edgeSet)
	}
	routeSet, err := routeTableSet(b, app.App, app.EdgeRouteTable)
	if err != nil {
		return nil, EdgeDelivery{}, err
	}
	if routeSet != nil {
		sets = append(sets, *routeSet)
		delivery.RouteTableKey = app.EdgeRouteTable.Location.Key
	}
	return sets, delivery, nil
}

func AssetSetResource(pending *PendingSets) infer.InferredResource {
	return infer.Resource(&assetSetResource{pending: pending})
}

type assetSetState struct {
	sdk.CustomResourceState
}

func DeclareAssetSets(pctx *sdk.Context, stack string, sets []AssetSet) ([]sdk.Resource, error) {
	declared := make([]sdk.Resource, 0, len(sets))
	for _, set := range sets {
		state := &assetSetState{}
		err := pctx.RegisterResource(AssetSetToken, set.name, sdk.Map{
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
