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

	t.Run("splits infra from app stacks", func(t *testing.T) {
		t.Parallel()

		got := classifyProjectStacks("shop", []string{
			"shop--infra",
			"shop--web--b1",
			"shop--api--b2",
		})
		want := ProjectTeardownPlan{
			InfraStack: "shop--infra",
			AppStacks:  []string{"shop--web--b1", "shop--api--b2"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("classifyProjectStacks = %+v, want %+v", got, want)
		}
	})

	t.Run("includes orphan app stacks", func(t *testing.T) {
		t.Parallel()

		got := classifyProjectStacks("shop", []string{"shop--web--b1", "shop--web--aborted"})
		want := []string{"shop--web--b1", "shop--web--aborted"}
		if !reflect.DeepEqual(got.AppStacks, want) {
			t.Fatalf("AppStacks = %v, want %v", got.AppStacks, want)
		}
		if got.InfraStack != "" {
			t.Errorf("InfraStack = %q, want empty", got.InfraStack)
		}
	})

	t.Run("excludes other projects and previews", func(t *testing.T) {
		t.Parallel()

		got := classifyProjectStacks("shop", []string{
			"shop--infra",
			"shop--web--b1",
			"shopfoo--infra",
			"shopfoo--web--b1",
			"other--infra",
			"shop-preview-pr1",
		})
		want := ProjectTeardownPlan{
			InfraStack: "shop--infra",
			AppStacks:  []string{"shop--web--b1"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("classifyProjectStacks = %+v, want %+v", got, want)
		}
	})

	t.Run("an app named infra is not the infra stack", func(t *testing.T) {
		t.Parallel()

		got := classifyProjectStacks("shop", []string{"shop--infra", "shop--infra--b1"})
		want := ProjectTeardownPlan{
			InfraStack: "shop--infra",
			AppStacks:  []string{"shop--infra--b1"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("classifyProjectStacks = %+v, want %+v", got, want)
		}
	})

	t.Run("no stacks is an empty plan", func(t *testing.T) {
		t.Parallel()

		got := classifyProjectStacks("shop", nil)
		if got.InfraStack != "" || len(got.AppStacks) != 0 {
			t.Fatalf("classifyProjectStacks(nil) = %+v, want empty plan", got)
		}
	})
}

func TestDestroyPhased(t *testing.T) {
	t.Parallel()

	t.Run("no infra stack starts before every app stack has finished", func(t *testing.T) {
		t.Parallel()

		var mu sync.Mutex
		var running, finished int
		var phases []string
		errs := destroyPhased(
			[]string{"a1", "a2", "a3", "a4", "a5", "a6"}, []string{"i1", "i2"},
			func(name string) error {
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
			func(name string) error {
				mu.Lock()
				defer mu.Unlock()
				phases = append(phases, "infra")
				if running != 0 || finished != 6 {
					return fmt.Errorf("infra stack %s started with %d app stacks in flight and %d done", name, running, finished)
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

	t.Run("stacks within a phase overlap up to the limit", func(t *testing.T) {
		t.Parallel()

		var inFlight, peak atomic.Int64
		full := make(chan struct{})
		var once sync.Once
		overlap := func(name string) error {
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
				return fmt.Errorf("%s ran without %d stacks in flight: the phase is serialized", name, teardownConcurrency)
			}
		}

		stacks := make([]string, teardownConcurrency*3)
		for i := range stacks {
			stacks[i] = fmt.Sprintf("shop--web--b%d", i)
		}
		if err := errors.Join(destroyPhased(stacks, nil, overlap, nil)...); err != nil {
			t.Fatal(err)
		}
		if got := peak.Load(); got > teardownConcurrency {
			t.Errorf("peak stacks in flight = %d, want at most %d", got, teardownConcurrency)
		}
	})

	t.Run("failures aggregate in stack order however they interleave", func(t *testing.T) {
		t.Parallel()

		apps := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8"}
		infra := []string{"i1", "i2"}
		want := make([]string, 0, len(apps)+len(infra))
		for _, name := range append(append([]string{}, apps...), infra...) {
			want = append(want, "destroy "+name)
		}

		fail := func(name string) error {
			time.Sleep(time.Duration(len(name)%3) * time.Millisecond)
			return fmt.Errorf("destroy %s", name)
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
