package deploy

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type TeardownConfig struct {
	Region      string
	BackendURL  string
	Passphrase  string
	ProjectName string
	StackName   string
	Pulumi      auto.PulumiCommand
	Stacks      StackIndex
	SkipRefresh bool

	realized *realizedStacks
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

func serializeReports(progress, log func(string)) (func(string), func(string)) {
	var mu sync.Mutex
	guard := func(f func(string)) func(string) {
		if f == nil {
			return nil
		}
		return func(msg string) {
			mu.Lock()
			defer mu.Unlock()
			f(msg)
		}
	}
	return guard(progress), guard(log)
}

func Destroy(ctx context.Context, cfg TeardownConfig, progress, log func(string)) error {
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
	}

	index, err := stackIndex(cfg.Stacks)
	if err != nil {
		return err
	}

	report(progress, "Selecting stack")
	stack, err := auto.SelectStackInlineSource(ctx, cfg.StackName, cfg.ProjectName, nil,
		auto.Pulumi(cfg.Pulumi),
		auto.SecretsProvider("passphrase"),
		auto.EnvVars(pulumiEnv(cfg.Region, cfg.BackendURL, cfg.Passphrase)),
	)
	if auto.IsSelectStack404Error(err) {
		report(progress, "No stack "+cfg.StackName+" to destroy")
		return index.RemoveStack(ctx, cfg.StackName)
	}
	if err != nil {
		return fmt.Errorf("select stack %s: %w", cfg.StackName, err)
	}

	report(progress, "Destroying resources (this can take several minutes)")
	logWriter := lineWriter(log)
	if _, err := stack.Destroy(ctx, destroyOptions(cfg, logWriter)...); err != nil {
		logWriter.Flush()
		return fmt.Errorf("destroy stack %s: %w%s", cfg.StackName, err, lockRecoveryHint(err, cfg))
	}
	logWriter.Flush()

	report(progress, "Removing stack")
	if err := stack.Workspace().RemoveStack(ctx, cfg.StackName); err != nil {
		return fmt.Errorf("remove stack %s: %w", cfg.StackName, err)
	}
	return index.RemoveStack(ctx, cfg.StackName)
}

func destroyOptions(cfg TeardownConfig, logWriter *lineForwarder) []optdestroy.Option {
	var opts []optdestroy.Option
	if !cfg.SkipRefresh && !cfg.realized.realizedHere(cfg.StackName) {
		opts = append(opts, optdestroy.Refresh())
	}
	if logWriter != nil {
		opts = append(opts, optdestroy.ProgressStreams(logWriter))
	}
	return opts
}

func lockRecoveryHint(err error, cfg TeardownConfig) string {
	if err == nil || !strings.Contains(err.Error(), "the stack is currently locked") {
		return ""
	}
	return fmt.Sprintf("\n\nthis lock outlives a run that was killed rather than one still working."+
		"\nconfirm no deploy or teardown is running against this stack, then release it with:"+
		"\n  PULUMI_BACKEND_URL=%s PULUMI_CONFIG_PASSPHRASE=<the account passphrase> pulumi cancel --stack %s"+
		"\nand re-run the teardown", cfg.BackendURL, cfg.StackName)
}

type PreviewStack struct {
	Identity  string
	Lifecycle deploymentsv1.Environment_Lifecycle
	Label     string
	CreatedAt int64
	ExpiresAt int64
}

func ListPreviewStacks(ctx context.Context, index StackIndex, slug string) ([]PreviewStack, error) {
	names, err := indexedStacks(ctx, index, slug)
	if err != nil {
		return nil, err
	}
	return previewStacksFromNames(slug, names), nil
}

func previewStacksFromNames(slug string, stackNames []string) []PreviewStack {
	plan := classifyPreviewStacks(slug, stackNames)
	persistent := map[string]struct{}{}
	for _, infra := range plan.InfraStacks {
		if pointer, _, ok := previewStackPointer(slug, infra); ok {
			persistent[pointer] = struct{}{}
		}
	}

	stacks := make([]PreviewStack, 0, len(plan.Pointers))
	for _, pointer := range plan.Pointers {
		lifecycle := deploymentsv1.Environment_LIFECYCLE_EPHEMERAL
		if _, ok := persistent[pointer]; ok {
			lifecycle = deploymentsv1.Environment_LIFECYCLE_PERSISTENT
		}
		stacks = append(stacks, PreviewStack{Identity: pointer, Lifecycle: lifecycle})
	}
	return stacks
}
