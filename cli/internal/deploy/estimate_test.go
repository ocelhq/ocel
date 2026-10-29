package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/workdir"
	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type pricedAfter struct {
	wait func(ctx context.Context)
	card pricing.Catalog
}

func (p pricedAfter) Lookup(ctx context.Context, queries []pricing.Query) ([]pricing.Answer, error) {
	p.wait(ctx)
	return p.card.Lookup(ctx, queries)
}

func isBuildOutputFree(projectDir string) bool {
	tried, cancel := context.WithCancel(context.Background())
	cancel()
	release, err := workdir.LockBuildOutput(tried, projectDir, func() {})
	if err != nil {
		return false
	}
	release()
	return true
}

func freedWhileShipped(ctx context.Context, projectDir string) bool {
	const watched = 500 * time.Millisecond
	deadline := time.Now().Add(estimateWait - time.Second)
	var shipped time.Time
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if shipped.IsZero() {
			if _, err := os.Stat(deployreport.Path(projectDir)); err == nil {
				shipped = time.Now()
			}
		}
		if !shipped.IsZero() {
			if isBuildOutputFree(projectDir) {
				return true
			}
			if time.Since(shipped) > watched {
				return false
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestADeployHoldsItsBuildOutputUntilItsEstimateIsDone(t *testing.T) {
	dependencies, fixture, console := reportingDeploy(t)
	console.Link(t, fixture.Root)
	var freed atomic.Bool
	console.Prices.Answer(pricedAfter{card: clitest.FakePriceCard(t), wait: func(ctx context.Context) {
		if freedWhileShipped(ctx, fixture.Root) {
			freed.Store(true)
		}
	}})
	dependencies.Prices.CachePath = filepath.Join(t.TempDir(), "answers.json")

	_, stderr, err := deployOnce(t, dependencies, fixture, deployOptions{})
	if err != nil {
		t.Fatalf("runDeploy err = %v; stderr=%s", err, stderr)
	}

	if freed.Load() {
		t.Error("the build output was released while the estimate was still taking inventory of it, want it held until the estimate is done")
	}
	if estimate := console.Reports()[0].GetDeployment().GetEstimate(); estimate == nil {
		t.Error("estimate = nil, want the estimate the deploy waited for")
	}
}

func TestAFailedDeployGivesUpItsEstimateInsteadOfWaitingForIt(t *testing.T) {
	dependencies, fixture, console := reportingDeploy(t)
	console.Link(t, fixture.Root)
	console.Prices.Answer(pricedAfter{card: clitest.FakePriceCard(t), wait: func(ctx context.Context) { <-ctx.Done() }})
	dependencies.Prices.CachePath = filepath.Join(t.TempDir(), "answers.json")
	fixture.Provider.FakeStacks().Entering(func(spec provider.StackSpec) error {
		if spec.Kind == provider.StackApp {
			return errors.New("simulated deploy failure")
		}
		return nil
	})

	started := time.Now()
	_, _, err := deployOnce(t, dependencies, fixture, deployOptions{})
	if err == nil {
		t.Fatal("runDeploy err = nil, want the simulated failure")
	}

	if waited := time.Since(started); waited >= estimateWait {
		t.Errorf("the failed deploy took %s, want it to give up its estimate rather than wait %s for it", waited, estimateWait)
	}
	if estimate := console.Reports()[0].GetDeployment().GetEstimate(); estimate != nil {
		t.Errorf("estimate = %v, want none on a failed deploy", estimate)
	}
}

func TestAnEstimateThatBreaksItsRulesIsLeftOffTheReport(t *testing.T) {
	broken := &costv1.Estimate{Currency: "USD", MonthlyFixed: "0.00", MonthlyUsage: "0.00"}

	kept, err := validateEstimate(broken, nil)

	if kept != nil || err == nil {
		t.Errorf("validateEstimate(an estimate with no profile) = %v, %v; want it dropped with the rule it breaks", kept, err)
	}
}
