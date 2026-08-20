package deploy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

type StackTeardown struct {
	Pulumi      PulumiAccess
	Project     string
	Stack       naming.StackName
	Stacks      StackIndex
	SkipRefresh bool
	Realized    *Realized
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
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
	}

	index, err := stackIndex(cfg.Stacks)
	if err != nil {
		return err
	}

	name := cfg.Stack.String()

	report(progress, "Selecting stack")
	stack, err := auto.SelectStackInlineSource(ctx, name, cfg.Pulumi.PulumiProject, nil, cfg.Pulumi.workspace()...)
	if auto.IsSelectStack404Error(err) {
		report(progress, "No stack "+name+" to destroy")
		return index.RemoveStack(ctx, cfg.Project, cfg.Stack)
	}
	if err != nil {
		return fmt.Errorf("select stack %s: %w", name, err)
	}

	report(progress, "Destroying resources (this can take several minutes)")
	logWriter := lineWriter(log)
	if _, err := stack.Destroy(ctx, destroyOptions(cfg, logWriter)...); err != nil {
		logWriter.Flush()
		return fmt.Errorf("destroy stack %s: %w%s", name, err, lockRecoveryHint(err, cfg))
	}
	logWriter.Flush()

	report(progress, "Removing stack")
	if err := stack.Workspace().RemoveStack(ctx, name); err != nil {
		return fmt.Errorf("remove stack %s: %w", name, err)
	}
	return index.RemoveStack(ctx, cfg.Project, cfg.Stack)
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

func destroyOptions(cfg StackTeardown, logWriter *lineForwarder) []optdestroy.Option {
	var opts []optdestroy.Option
	if !cfg.SkipRefresh && !cfg.Realized.realizedHere(cfg.Project, cfg.Stack) {
		opts = append(opts, optdestroy.Refresh())
	}
	if logWriter != nil {
		opts = append(opts, optdestroy.ProgressStreams(logWriter))
	}
	return opts
}

func lockRecoveryHint(err error, cfg StackTeardown) string {
	if err == nil || !strings.Contains(err.Error(), "the stack is currently locked") {
		return ""
	}
	return fmt.Sprintf("\n\nthis lock outlives a run that was killed rather than one still working."+
		"\nconfirm no deploy or teardown is running against this stack, then release it with:"+
		"\n  PULUMI_BACKEND_URL=%s PULUMI_CONFIG_PASSPHRASE=<the account passphrase> pulumi cancel --stack %s"+
		"\nand re-run the teardown", cfg.Pulumi.BackendURL, cfg.Stack)
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
