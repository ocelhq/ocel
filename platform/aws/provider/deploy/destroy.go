package deploy

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	kitpulumi "github.com/ocelhq/ocel/pkg/providerkit/pulumi"
)

type StackTeardown struct {
	Pulumi   PulumiAccess
	Project  string
	Stack    naming.StackName
	Stacks   StackIndex
	Realized *Realized
	engine   kitpulumi.Engine
}

func nilSafe(progress func(string)) func(string) {
	return func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}
}

const teardownConcurrency = 4

func runBounded[T any](limit int, items []T, run func(T) error) []error {
	errs := make([]error, len(items))
	var wg sync.WaitGroup
	slots := make(chan struct{}, limit)
	for i, item := range items {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			errs[i] = run(item)
		}()
	}
	wg.Wait()
	return errs
}

func Destroy(ctx context.Context, cfg StackTeardown, progress, log func(string)) error {
	ref := providerkit.StackRef{Project: cfg.Project, Name: cfg.Stack}
	release := releaserFor(cfg.Pulumi, cfg.Stacks, cfg.Realized, cfg.engine)
	return release.Destroy(ctx, ref, reporterFor(nil, StageID{}, progress, log))
}

func destroyStackStage(ctx context.Context, t Teardown, stack naming.StackName, stage Stage, kind string, log func(string)) (err error) {
	start := time.Now()
	defer func() { spanForStage(t.Report.Tracer, stage, start, time.Now(), err) }()

	report := t.Report.stage(stage)
	report(sanitizeMessage("Destroying " + kind + " stack " + stack.String()))
	if derr := Destroy(ctx, t.forStack(stack), report, log); derr != nil {
		err = fmt.Errorf("destroy %s stack %s: %w", kind, stack, derr)
		return err
	}
	return nil
}

type PreviewStack struct {
	Identity  string
	Lifecycle environmentv1.Lifecycle
	Label     string
	CreatedAt int64
	ExpiresAt int64
}

func ListPreviewStacks(ctx context.Context, index StackIndex, slug string) ([]PreviewStack, error) {
	stacks, err := indexedStacks(ctx, index, slug)
	if err != nil {
		return nil, err
	}
	return previewStacksFromNames(stacks), nil
}

func previewStacksFromNames(stacks []naming.StackName) []PreviewStack {
	plan := classifyPreviewStacks(stacks)
	persistent := map[string]struct{}{}
	for _, infra := range plan.InfraStacks {
		persistent[infra.Env] = struct{}{}
	}

	previews := make([]PreviewStack, 0, len(plan.Pointers))
	for _, pointer := range plan.Pointers {
		lifecycle := environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL
		if _, ok := persistent[pointer]; ok {
			lifecycle = environmentv1.Lifecycle_LIFECYCLE_PERSISTENT
		}
		previews = append(previews, PreviewStack{Identity: pointer, Lifecycle: lifecycle})
	}
	return previews
}
