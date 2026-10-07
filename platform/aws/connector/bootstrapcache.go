package main

import (
	"context"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

const bootstrapTTL = 5 * time.Minute

type bootstrapCache struct {
	namespace bootstrap.Namespace
	stacks    cfn.StacksAPI
	now       func() time.Time

	mu   sync.Mutex
	read map[environment.Tier]cachedBootstrap
}

type cachedBootstrap struct {
	deployed bootstrap.Deployed
	at       time.Time
}

func (d *bootstrapCache) resolve(ctx context.Context, tier environment.Tier) (bootstrap.Deployed, error) {
	d.mu.Lock()
	memo, known := d.read[tier]
	d.mu.Unlock()
	if known && d.now().Sub(memo.at) < bootstrapTTL {
		return memo.deployed, nil
	}
	deployed, err := bootstrap.CheckDeployedFor(ctx, d.stacks, d.namespace, tier)
	if err != nil {
		return bootstrap.Deployed{}, err
	}
	d.mu.Lock()
	d.read[tier] = cachedBootstrap{deployed: deployed, at: d.now()}
	d.mu.Unlock()
	return deployed, nil
}

func (d *bootstrapCache) Table(ctx context.Context, tier environment.Tier) (string, error) {
	deployed, err := d.resolve(ctx, tier)
	return deployed.StateTable, err
}

func (d *bootstrapCache) ValuesTable(ctx context.Context, tier environment.Tier) (string, error) {
	deployed, err := d.resolve(ctx, tier)
	return deployed.VariablesTable, err
}

func (d *bootstrapCache) Key(ctx context.Context, tier environment.Tier) (string, error) {
	deployed, err := d.resolve(ctx, tier)
	return deployed.VariablesKeyARN, err
}
