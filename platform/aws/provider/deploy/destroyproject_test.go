package deploy

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
)

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
		appErrs, infraErrs := destroyPhased(
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

		if err := errors.Join(append(append([]error{}, appErrs...), infraErrs...)...); err != nil {
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
		appErrs, _ := destroyPhased(stacks, nil, overlap, nil)
		if err := errors.Join(appErrs...); err != nil {
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
			appErrs, infraErrs := destroyPhased(apps, infra, fail, fail)
			for _, err := range append(append([]error{}, appErrs...), infraErrs...) {
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

	t.Run("a trailing slash scopes to the project in one environment", func(t *testing.T) {
		t.Parallel()

		if p := projectEnvPrefix("prod", "shop"); p != "prod/shop/" {
			t.Errorf("projectEnvPrefix = %q, want prod/shop/", p)
		}
		if p := projectEnvPrefix("pr-7", "shop"); p != "pr-7/shop/" {
			t.Errorf("projectEnvPrefix = %q, want pr-7/shop/", p)
		}
	})

	t.Run("every environment the index knows is purged, not just the configured one", func(t *testing.T) {
		t.Parallel()

		release := naming.NewRelease("b1", "")
		envs := purgeEnvs([]naming.StackName{
			naming.InfraStack("prod"),
			naming.AppStack("pr-7", "web", release),
			naming.AppStack("pr-7", "admin", release),
		}, "prod")

		if want := []string{"pr-7", "prod"}; !reflect.DeepEqual(envs, want) {
			t.Errorf("purgeEnvs = %v, want %v", envs, want)
		}
	})

	t.Run("the configured environment is purged even when the index is empty", func(t *testing.T) {
		t.Parallel()

		if envs := purgeEnvs(nil, "prod"); !reflect.DeepEqual(envs, []string{"prod"}) {
			t.Errorf("purgeEnvs = %v, want [prod]", envs)
		}
	})
}
