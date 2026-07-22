// This file holds the teardown and enumeration counterparts to Run. Like Run,
// their bodies drive the real Pulumi Automation API and are exercised only by
// an opt-in run against a live account, never by unit tests.
package deploy

import (
	"context"
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// TeardownConfig carries what a Destroy needs to reach and remove one stack:
// the Pulumi backend, its decryption passphrase, and the exact stack to act on.
// The account-global backend holds every project's stacks, so StackName is the
// project-scoped "<projectID>-preview-<identity>" the server derives (see the
// server's stackName), never an identity alone.
type TeardownConfig struct {
	Region      string
	BackendURL  string
	Passphrase  string
	ProjectName string // Pulumi project, e.g. "ocel"
	StackName   string // exact "<projectID>-preview-<identity>"
	Pulumi      auto.PulumiCommand
}

// nilSafe wraps a progress callback so callers can report unconditionally: a
// nil callback makes reporting a no-op.
func nilSafe(progress func(string)) func(string) {
	return func(msg string) {
		if progress != nil {
			progress(msg)
		}
	}
}

// Destroy tears down one stack — a `pulumi destroy` followed by removing the
// stack from the backend — and streams progress. progress and log may be nil.
// Destroy performs the real teardown and is not exercised by unit tests.
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
		auto.EnvVars(map[string]string{
			"PULUMI_BACKEND_URL":       cfg.BackendURL,
			"PULUMI_CONFIG_PASSPHRASE": cfg.Passphrase,
			"AWS_REGION":               cfg.Region,
		}),
	)
	if err != nil {
		return fmt.Errorf("select stack %s: %w", cfg.StackName, err)
	}

	report(progress, "Destroying resources (this can take several minutes)")
	logWriter := lineWriter(log)
	// Refresh first so the destroy reconciles against real provider state — this
	// clears the pending operations an interrupted earlier deploy can leave on a
	// stack, which would otherwise make the destroy refuse.
	destroyOpts := []optdestroy.Option{optdestroy.Refresh()}
	if logWriter != nil {
		destroyOpts = append(destroyOpts, optdestroy.ProgressStreams(logWriter))
	}
	if _, err := stack.Destroy(ctx, destroyOpts...); err != nil {
		logWriter.Flush()
		return fmt.Errorf("destroy stack %s: %w", cfg.StackName, err)
	}
	logWriter.Flush()

	report(progress, "Removing stack")
	if err := stack.Workspace().RemoveStack(ctx, cfg.StackName); err != nil {
		return fmt.Errorf("remove stack %s: %w", cfg.StackName, err)
	}
	return nil
}

// PreviewStack is one enumerated preview-class stack, in the pure shape
// ListPreviewStacks returns and the server maps to a PreviewEnvironment.
type PreviewStack struct {
	Identity  string
	Lifecycle deploymentsv1.Environment_Lifecycle
	Label     string
	CreatedAt int64
	ExpiresAt int64
}

// ListConfig carries what enumerating a project's preview stacks needs: the
// Pulumi backend, the Pulumi project the workspace opens under, and the manifest
// ProjectID the account-global backend's stacks are filtered by.
type ListConfig struct {
	Region      string
	BackendURL  string
	Passphrase  string
	ProjectName string // Pulumi project, e.g. "ocel"
	ProjectID   string // manifest project id, the stack-name scope prefix
	Pulumi      auto.PulumiCommand
}

// ListPreviewStacks enumerates one project's preview ENVIRONMENTS from the
// preview Pulumi backend — one PreviewStack per distinct pointer, filtered to
// cfg.ProjectID so it never lists another project's previews. It reads the real
// Pulumi backend and is not exercised by unit tests; the pure pointer
// enumeration and lifecycle inference it relies on is previewStacksFromNames.
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
	return previewStacksFromNames(cfg.ProjectID, names), nil
}

// previewStacksFromNames collapses a project's preview stack names into one
// PreviewStack per distinct pointer (a preview instance holds many pointers,
// each owning several stacks). Lifecycle is inferred from the stack list alone:
// a pointer that owns a per-name "--infra" stack is persistent, one with only
// app-deploy stacks is ephemeral. Entries come back sorted by pointer.
//
// Label, CreatedAt, and ExpiresAt are not recoverable from stack names — they
// are stamped as stack tags at deploy time, whose reading is the opt-in-e2e
// seam (bd ocelhq-d7u); until it lands, those fields stay zero. Pure.
func previewStacksFromNames(projectID string, stackNames []string) []PreviewStack {
	plan := classifyPreviewStacks(projectID, stackNames)
	persistent := map[string]struct{}{}
	for _, infra := range plan.InfraStacks {
		if pointer, _, ok := previewStackPointer(projectID, infra); ok {
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

// backendWorkspace opens a Pulumi Automation API workspace over the given
// self-managed backend, for stack enumeration/selection (no inline program).
// Shared by preview enumeration and whole-project teardown.
func backendWorkspace(ctx context.Context, project, backendURL, passphrase, region string, pulumiCmd auto.PulumiCommand) (auto.Workspace, error) {
	ws, err := auto.NewLocalWorkspace(ctx,
		auto.Project(workspace.Project{
			Name:    tokens.PackageName(project),
			Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		}),
		auto.Pulumi(pulumiCmd),
		auto.SecretsProvider("passphrase"),
		auto.EnvVars(map[string]string{
			"PULUMI_BACKEND_URL":       backendURL,
			"PULUMI_CONFIG_PASSPHRASE": passphrase,
			"AWS_REGION":               region,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	return ws, nil
}
