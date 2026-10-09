package pulumi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const lockProbeProject = "ocel-lock-probe"

func lockProbeRef(env string) provider.StackRef {
	return provider.StackRef{Project: "probe", Tier: environment.TierPreview, Name: naming.InfraStack(env)}
}

func leaseOver(ctx context.Context, env string) context.Context {
	return provider.WithLease(ctx, provider.Lease{Tier: environment.TierPreview, Project: "probe", Env: env})
}

func plantLock(t *testing.T, dir string, ref provider.StackRef) string {
	t.Helper()
	locks := filepath.Join(dir, ".pulumi", "locks", "organization", lockProbeProject, ref.Name.String())
	if err := os.MkdirAll(locks, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(locks, "4b1f6c2e-killed-run.json")
	content := `{"pid":4242,"username":"ci","hostname":"killed","timestamp":"2026-10-09T00:00:00Z"}`
	if err := os.WriteFile(lock, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return lock
}

func TestADestroyUnderTheLeaseReleasesTheLockAKilledRunLeftAndTearsTheStackDown(t *testing.T) {
	if testing.Short() {
		t.Skip("this drives the real engine, which installs and runs the pinned Pulumi runtime")
	}

	ctx := context.Background()
	dir := t.TempDir()
	automation := pulumi.New(pulumi.Config{
		Backend: pulumi.Backend{URL: "file://" + dir, Passphrase: "a-passphrase", Project: lockProbeProject},
		Program: program{}.Run,
	})
	locked, other := lockProbeRef("pr-7"), lockProbeRef("pr-8")
	for _, ref := range []provider.StackRef{locked, other} {
		if _, err := automation.Run(ctx, provider.StackSpec{Ref: ref, Kind: provider.StackInfra}, nil); err != nil {
			t.Fatalf("Run(%s) = %v", ref.Name, err)
		}
	}
	plantLock(t, dir, locked)
	otherLock := plantLock(t, dir, other)

	said := &sayings{Log: progress.Discard()}
	if err := automation.Destroy(leaseOver(ctx, "pr-7"), locked, said); err != nil {
		t.Fatalf("Destroy() under the lease over a stack a killed run left locked = %v", err)
	}
	if !slices.ContainsFunc(said.said, func(line string) bool {
		return strings.Contains(line, "Released the lock an interrupted run left on stack "+locked.Name.String())
	}) {
		t.Errorf("Destroy() said %q, want a line saying it released the lock an interrupted run left", said.said)
	}
	if _, err := os.Stat(otherLock); err != nil {
		t.Errorf("the lock on stack %s, which the destroy never touched, is gone: %v", other.Name, err)
	}
}

func TestADestroyWithoutTheLeaseStillRefusesALockedStack(t *testing.T) {
	if testing.Short() {
		t.Skip("this drives the real engine, which installs and runs the pinned Pulumi runtime")
	}

	ctx := context.Background()
	dir := t.TempDir()
	automation := pulumi.New(pulumi.Config{
		Backend: pulumi.Backend{URL: "file://" + dir, Passphrase: "a-passphrase", Project: lockProbeProject},
		Program: program{}.Run,
	})
	ref := lockProbeRef("pr-7")
	if _, err := automation.Run(ctx, provider.StackSpec{Ref: ref, Kind: provider.StackInfra}, nil); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	lock := plantLock(t, dir, ref)

	for name, ctx := range map[string]context.Context{
		"no lease":               ctx,
		"a lease on another env": leaseOver(ctx, "pr-8"),
	} {
		err := automation.Destroy(ctx, ref, nil)
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
			t.Fatalf("%s: Destroy() over a locked stack = %v, want a busy refusal", name, err)
		}
		if !strings.Contains(refused.Message, "pulumi cancel --stack "+ref.Name.String()) {
			t.Errorf("%s: the refusal reads %q, want it to name the command that releases the lock", name, refused.Message)
		}
		if _, err := os.Stat(lock); err != nil {
			t.Errorf("%s: the lock was removed by an operation that holds no lease over the stack: %v", name, err)
		}
	}
}

type lockedEngine struct {
	recordingEngine
	locks    int
	attempts int
	unlocked []string
}

func (e *lockedEngine) attempt() error {
	e.attempts++
	if e.locks > 0 {
		e.locks--
		return errors.New("the stack is currently locked by 1 lock(s): either the stack is in use or a previous update was interrupted")
	}
	return nil
}

func (e *lockedEngine) Destroy(context.Context, pulumi.WorkspaceSpec, progress.Log) error {
	return e.attempt()
}

func (e *lockedEngine) Up(context.Context, pulumi.WorkspaceSpec, progress.Log) (auto.OutputMap, error) {
	return nil, e.attempt()
}

func (e *lockedEngine) Preview(context.Context, pulumi.WorkspaceSpec, pulumi.Operation, progress.Log) ([]provider.Change, error) {
	return nil, e.attempt()
}

func (e *lockedEngine) Unlock(_ context.Context, setup pulumi.WorkspaceSpec) error {
	e.unlocked = append(e.unlocked, setup.Stack)
	return nil
}

func leasedSpec() context.Context {
	return provider.WithLease(context.Background(), provider.Lease{Tier: spec().Ref.Tier, Project: spec().Ref.Project, Env: spec().Ref.Name.Env})
}

func TestEachLeasedOperationReleasesAStaleLockAndRetriesOnce(t *testing.T) {
	t.Parallel()

	for name, run := range map[string]func(*pulumi.Automation) error{
		"provision": func(a *pulumi.Automation) error { _, err := a.Run(leasedSpec(), spec(), nil); return err },
		"destroy":   func(a *pulumi.Automation) error { return a.Destroy(leasedSpec(), spec().Ref, nil) },
		"plan":      func(a *pulumi.Automation) error { _, err := a.Preview(leasedSpec(), spec(), nil); return err },
	} {
		engine := &lockedEngine{locks: 1}
		if err := run(pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run, Engine: engine})); err != nil {
			t.Errorf("%s: under the lease over a stale lock = %v, want it to run once the lock is released", name, err)
		}
		if engine.attempts != 2 || !slices.Equal(engine.unlocked, []string{spec().Ref.Name.String()}) {
			t.Errorf("%s: ran %d times and released %q, want two runs around one release of %s", name, engine.attempts, engine.unlocked, spec().Ref.Name)
		}
	}
}

func TestALockThatComesBackAfterItsReleaseReadsAsBusy(t *testing.T) {
	t.Parallel()

	engine := &lockedEngine{locks: 2}
	err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run, Engine: engine}).
		Destroy(leasedSpec(), spec().Ref, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Destroy() over a stack locked again after its release = %v, want a busy refusal", err)
	}
	if engine.attempts != 2 || len(engine.unlocked) != 1 {
		t.Errorf("ran %d times and released %d times, want one retry after one release", engine.attempts, len(engine.unlocked))
	}
}

func TestAnOperationWithoutTheLeaseNeverReleasesALock(t *testing.T) {
	t.Parallel()

	engine := &lockedEngine{locks: 1}
	err := pulumi.New(pulumi.Config{Backend: backend(), Program: program{}.Run, Engine: engine}).
		Destroy(context.Background(), spec().Ref, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeBusy {
		t.Fatalf("Destroy() over a locked stack without the lease = %v, want a busy refusal", err)
	}
	if engine.attempts != 1 || len(engine.unlocked) != 0 {
		t.Errorf("ran %d times and released %d times, want one run and no release", engine.attempts, len(engine.unlocked))
	}
}
