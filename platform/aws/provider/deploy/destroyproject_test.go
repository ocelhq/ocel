package deploy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestRootStackWorkerNames(t *testing.T) {
	t.Parallel()

	t.Run("covers every app from history not the shared store", func(t *testing.T) {
		t.Parallel()

		f := &recordingRootStack{
			history: []edge.HistoryEntry{
				{Promotion: edge.Promotion{PromotionID: "p2", Builds: map[string]string{"web": "b2", "api": "b2"}}},
				{Promotion: edge.Promotion{PromotionID: "p1", Builds: map[string]string{"web": "b1"}}},
			},
		}
		ctx := context.Background()
		state, err := f.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "proj-x"}, nil)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		names, err := rootStackWorkerNames(ctx, f, state, "proj_x", "prod")
		if err != nil {
			t.Fatalf("rootStackWorkerNames: %v", err)
		}

		prod := "proj_x-prod"
		for _, want := range []string{
			legacyWorkerName(prod),
			workerScriptName("proj_x", "prod", "root"),
			workerScriptName("proj_x", "prod", "web"),
			workerScriptName("proj_x", "prod", "api"),
		} {
			if !contains(names, want) {
				t.Errorf("worker names %v missing %q", names, want)
			}
		}

		for _, n := range names {
			if n == "ocel-deployments-store" {
				t.Errorf("worker names %v must not include the shared store worker", names)
			}
		}

		seen := map[string]bool{}
		for _, n := range names {
			if seen[n] {
				t.Errorf("duplicate worker name %q in %v", n, names)
			}
			seen[n] = true
		}
	})

	t.Run("propagates the history error", func(t *testing.T) {
		t.Parallel()

		_, err := rootStackWorkerNames(context.Background(), &recordingRootStack{}, edge.RootStackState{}, "proj_x", "prod")
		if err == nil {
			t.Fatal("rootStackWorkerNames with an unreadable store err = nil, want the history error")
		}
	})
}

func TestClassifyProjectStacks(t *testing.T) {
	t.Parallel()

	web := naming.AppStack(ProductionEnv, "web", naming.NewRelease("b1", ""))
	api := naming.AppStack(ProductionEnv, "api", naming.NewRelease("b2", ""))

	t.Run("splits infra from app stacks", func(t *testing.T) {
		t.Parallel()

		got := classifyProjectStacks([]naming.StackName{naming.InfraStack(ProductionEnv), web, api})
		want := ProjectTeardownPlan{
			InfraStack: naming.InfraStack(ProductionEnv),
			AppStacks:  []naming.StackName{web, api},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("classifyProjectStacks = %+v, want %+v", got, want)
		}
	})

	t.Run("includes orphan app stacks", func(t *testing.T) {
		t.Parallel()

		aborted := naming.AppStack(ProductionEnv, "web", naming.NewRelease("aborted", ""))
		got := classifyProjectStacks([]naming.StackName{web, aborted})
		want := []naming.StackName{web, aborted}
		if !reflect.DeepEqual(got.AppStacks, want) {
			t.Fatalf("AppStacks = %v, want %v", got.AppStacks, want)
		}
		if !got.InfraStack.IsZero() {
			t.Errorf("InfraStack = %q, want none", got.InfraStack)
		}
	})

	t.Run("excludes previews", func(t *testing.T) {
		t.Parallel()

		got := classifyProjectStacks([]naming.StackName{
			naming.InfraStack(ProductionEnv),
			web,
			naming.InfraStack("pr-1"),
			naming.AppStack("pr-1", "web", naming.NewRelease("b1", "")),
		})
		want := ProjectTeardownPlan{
			InfraStack: naming.InfraStack(ProductionEnv),
			AppStacks:  []naming.StackName{web},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("classifyProjectStacks = %+v, want %+v", got, want)
		}
	})

	t.Run("no stacks is an empty plan", func(t *testing.T) {
		t.Parallel()

		got := classifyProjectStacks(nil)
		if !got.InfraStack.IsZero() || len(got.AppStacks) != 0 {
			t.Fatalf("classifyProjectStacks(nil) = %+v, want empty plan", got)
		}
	})
}

func appStacks(names ...string) []naming.StackName {
	stacks := make([]naming.StackName, 0, len(names))
	for _, name := range names {
		stacks = append(stacks, naming.AppStack(ProductionEnv, name, naming.NewRelease(name, "")))
	}
	return stacks
}

func TestDestroyPhased(t *testing.T) {
	t.Parallel()

	t.Run("infra waits for every app stack", func(t *testing.T) {
		t.Parallel()

		var mu sync.Mutex
		var running, finished int
		var phases []string
		errs := destroyPhased(
			appStacks("a1", "a2", "a3", "a4", "a5", "a6"),
			[]naming.StackName{naming.InfraStack(ProductionEnv), naming.InfraStack("pr-1")},
			func(naming.StackName) error {
				mu.Lock()
				running++
				phases = append(phases, "app")
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				running--
				finished++
				mu.Unlock()
				return nil
			},
			func(stack naming.StackName) error {
				mu.Lock()
				defer mu.Unlock()
				phases = append(phases, "infra")
				if running != 0 || finished != 6 {
					return fmt.Errorf("infra stack %s started with %d app stacks in flight and %d done", stack, running, finished)
				}
				return nil
			})

		if err := errors.Join(errs...); err != nil {
			t.Fatalf("destroyPhased crossed the phase barrier: %v", err)
		}
		if want := []string{"app", "app", "app", "app", "app", "app", "infra", "infra"}; !reflect.DeepEqual(phases, want) {
			t.Errorf("phase order = %v, want %v", phases, want)
		}
	})

	t.Run("a phase overlaps up to the limit", func(t *testing.T) {
		t.Parallel()

		var inFlight, peak atomic.Int64
		full := make(chan struct{})
		var once sync.Once
		overlap := func(stack naming.StackName) error {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				peaked := peak.Load()
				if n <= peaked || peak.CompareAndSwap(peaked, n) {
					break
				}
			}
			if n == teardownConcurrency {
				once.Do(func() { close(full) })
			}
			select {
			case <-full:
				return nil
			case <-time.After(5 * time.Second):
				once.Do(func() { close(full) })
				return fmt.Errorf("%s ran without %d stacks in flight: the phase is serialized", stack, teardownConcurrency)
			}
		}

		stacks := make([]naming.StackName, teardownConcurrency*3)
		for i := range stacks {
			stacks[i] = naming.AppStack(ProductionEnv, "web", naming.NewRelease(fmt.Sprintf("b%d", i), ""))
		}
		if err := errors.Join(destroyPhased(stacks, nil, overlap, nil)...); err != nil {
			t.Fatal(err)
		}
		if got := peak.Load(); got > teardownConcurrency {
			t.Errorf("peak stacks in flight = %d, want at most %d", got, teardownConcurrency)
		}
	})

	t.Run("failures aggregate in stack order", func(t *testing.T) {
		t.Parallel()

		apps := appStacks("a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8")
		infra := []naming.StackName{naming.InfraStack(ProductionEnv), naming.InfraStack("pr-1")}
		want := make([]string, 0, len(apps)+len(infra))
		for _, stack := range append(append([]naming.StackName{}, apps...), infra...) {
			want = append(want, "destroy "+stack.String())
		}

		fail := func(stack naming.StackName) error {
			time.Sleep(time.Duration(len(stack.App)%3) * time.Millisecond)
			return fmt.Errorf("destroy %s", stack)
		}
		for range 20 {
			var got []string
			for _, err := range destroyPhased(apps, infra, fail, fail) {
				got = append(got, err.Error())
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("aggregated failures = %v, want %v", got, want)
			}
		}
	})
}

func TestProjectPrefixes(t *testing.T) {
	t.Parallel()

	t.Run("a trailing slash scopes to the project", func(t *testing.T) {
		t.Parallel()

		if p := projectAssetR2Prefix("shop"); p != "assets/shop/" {
			t.Errorf("projectAssetR2Prefix = %q, want assets/shop/", p)
		}
		if p := projectISRPrefix("prod", "shop"); p != "prod/shop/" {
			t.Errorf("projectISRPrefix = %q, want prod/shop/", p)
		}
		if p := projectEdgeR2Prefix("shop"); p != "edge/shop/" {
			t.Errorf("projectEdgeR2Prefix = %q, want edge/shop/", p)
		}
		if p := projectImageConfigPrefix("shop"); p != "image-config/shop/" {
			t.Errorf("projectImageConfigPrefix = %q, want image-config/shop/", p)
		}
	})
}
