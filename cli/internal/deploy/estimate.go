package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"buf.build/go/protovalidate"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	estimateWait          = 5 * time.Second
	maxEstimatedResources = 2000
)

var errTooManyResources = errors.New("the estimate prices more resources than a deployment report carries")

type deployEstimate struct {
	done     chan struct{}
	cancel   context.CancelFunc
	estimate *costv1.Estimate
}

func startEstimate(ctx context.Context, dependencies Dependencies, span *run.Span, provider *providerprocess.Provider, cfg *project.Project, manifest *contractv1.Manifest, env *environmentv1.Environment) *deployEstimate {
	if !provider.Facts().GetTakesInventory() {
		return nil
	}
	if linked, err := console.IsLinked(cfg.Dir); err != nil || !linked {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	ctx, traced := span.Trace(ctx, cfg.Slug, "estimate_monthly_cost")
	e := &deployEstimate{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(e.done)
		estimate, err := validateEstimate(estimateDeploy(ctx, dependencies, provider, cfg, manifest, env))
		traced.End(err)
		if err == nil {
			e.estimate = estimate
		}
	}()
	return e
}

func (e *deployEstimate) finish(deployed bool) *costv1.Estimate {
	if e == nil {
		return nil
	}
	defer func() {
		e.cancel()
		<-e.done
	}()
	if !deployed {
		return nil
	}
	timer := time.NewTimer(estimateWait)
	defer timer.Stop()
	select {
	case <-e.done:
		return e.estimate
	case <-timer.C:
		return nil
	}
}

func validateEstimate(estimate *costv1.Estimate, err error) (*costv1.Estimate, error) {
	if err != nil {
		return nil, err
	}
	if err := protovalidate.Validate(estimate); err != nil {
		return nil, fmt.Errorf("the estimate breaks the rules of a deployment report: %w", err)
	}
	return estimate, nil
}

func estimateDeploy(ctx context.Context, dependencies Dependencies, provider *providerprocess.Provider, cfg *project.Project, manifest *contractv1.Manifest, env *environmentv1.Environment) (*costv1.Estimate, error) {
	set, err := provider.Inventory(ctx, cfg, manifest, env)
	if err != nil {
		return nil, fmt.Errorf("take inventory: %w", err)
	}
	if len(set.GetResources()) > maxEstimatedResources {
		return nil, errTooManyResources
	}
	catalog, err := dependencies.OpenPriceCatalog(false)
	if err != nil {
		return nil, err
	}
	return pricing.Estimate(ctx, catalog, dependencies.Prices.Table, set, &costv1.Usage{Profile: pricing.DefaultProfile})
}
