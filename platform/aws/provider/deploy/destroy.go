package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type TeardownConfig struct {
	Region      string
	BackendURL  string
	Passphrase  string
	ProjectName string
	StackName   string
	Pulumi      auto.PulumiCommand
}

func nilSafe(progress func(string)) func(string) {
	return func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}
}

func Destroy(ctx context.Context, cfg TeardownConfig, progress, log func(string)) error {
	report := func(f func(string), msg string) {
		if f != nil {
			f(msg)
		}
	}

	report(progress, "Selecting stack")
	stack, err := auto.SelectStackInlineSource(ctx, cfg.StackName, cfg.ProjectName, nil,
		auto.Pulumi(cfg.Pulumi),
		auto.SecretsProvider("passphrase"),
		auto.EnvVars(pulumiEnv(cfg.Region, cfg.BackendURL, cfg.Passphrase)),
	)
	if auto.IsSelectStack404Error(err) {
		report(progress, "No stack "+cfg.StackName+" to destroy")
		return nil
	}
	if err != nil {
		return fmt.Errorf("select stack %s: %w", cfg.StackName, err)
	}

	report(progress, "Destroying resources (this can take several minutes)")
	logWriter := lineWriter(log)
	destroyOpts := []optdestroy.Option{optdestroy.Refresh()}
	if logWriter != nil {
		destroyOpts = append(destroyOpts, optdestroy.ProgressStreams(logWriter))
	}
	if _, err := stack.Destroy(ctx, destroyOpts...); err != nil {
		logWriter.Flush()
		return fmt.Errorf("destroy stack %s: %w%s", cfg.StackName, err, lockRecoveryHint(err, cfg))
	}
	logWriter.Flush()

	report(progress, "Removing stack")
	if err := stack.Workspace().RemoveStack(ctx, cfg.StackName); err != nil {
		return fmt.Errorf("remove stack %s: %w", cfg.StackName, err)
	}
	return nil
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

type ListConfig struct {
	Region      string
	BackendURL  string
	Passphrase  string
	ProjectName string
	Slug        string
	Pulumi      auto.PulumiCommand
}

func ListPreviewStacks(ctx context.Context, cfg ListConfig) ([]PreviewStack, error) {
	ws, err := backendWorkspace(ctx, cfg.ProjectName, cfg.BackendURL, cfg.Passphrase, cfg.Region, cfg.Pulumi)
	if err != nil {
		return nil, err
	}

	summaries, err := ws.ListStacks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stacks: %w", err)
	}

	names := make([]string, len(summaries))
	for i, s := range summaries {
		names[i] = s.Name
	}
	return previewStacksFromNames(cfg.Slug, names), nil
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

func backendWorkspace(ctx context.Context, project, backendURL, passphrase, region string, pulumiCmd auto.PulumiCommand) (auto.Workspace, error) {
	if project == "" {
		return nil, fmt.Errorf("open workspace: no project name")
	}
	ws, err := auto.NewLocalWorkspace(ctx,
		auto.Project(workspace.Project{
			Name:    tokens.PackageName(project),
			Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		}),
		auto.Pulumi(pulumiCmd),
		auto.SecretsProvider("passphrase"),
		auto.EnvVars(pulumiEnv(region, backendURL, passphrase)),
	)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	return ws, nil
}
