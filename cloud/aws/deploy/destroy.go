// This file holds the teardown and enumeration counterparts to Run. Like Run,
// their bodies drive the real Pulumi Automation API and are exercised only by
// an opt-in run against a live account, never by unit tests.
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
	destroyOpts := []optdestroy.Option{}
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

// previewStackInfix marks a preview-class stack name: "<projectID>-preview-<identity>".
const previewStackInfix = "-preview-"

// ListPreviewStacks enumerates one project's preview-class stacks in the
// account-global Pulumi backend, one PreviewStack each. It filters to
// cfg.ProjectID so it never lists another project's previews. It reads the real
// Pulumi backend and is not exercised by unit tests; the pure name→identity
// extraction it relies on is (previewIdentityFromStack).
//
// The Pulumi stack summary carries the stack name and last-update time but not
// an environment's lifecycle or PR label — those are stamped as stack tags at
// deploy time. Reading those tags is the opt-in-e2e seam; until it lands, an
// enumerated stack reports its identity with an unspecified lifecycle and no
// label.
func ListPreviewStacks(ctx context.Context, cfg ListConfig) ([]PreviewStack, error) {
	ws, err := previewWorkspace(ctx, cfg.ProjectName, cfg.BackendURL, cfg.Passphrase, cfg.Region, cfg.Pulumi)
	if err != nil {
		return nil, err
	}

	summaries, err := ws.ListStacks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list stacks: %w", err)
	}

	var stacks []PreviewStack
	for _, s := range summaries {
		identity, ok := previewIdentityFromStack(cfg.ProjectID, s.Name)
		if !ok {
			continue
		}
		stacks = append(stacks, PreviewStack{Identity: identity})
	}
	return stacks, nil
}

// previewWorkspace opens a Pulumi Automation API workspace over the given
// self-managed backend, for stack enumeration/selection (no inline program).
func previewWorkspace(ctx context.Context, project, backendURL, passphrase, region string, pulumiCmd auto.PulumiCommand) (auto.Workspace, error) {
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

// previewIdentityFromStack extracts a preview environment's identity from a
// stack name of the form "<projectID>-preview-<identity>", or reports ok=false
// for any stack that isn't a preview of projectID (including another project's
// previews). It is pure.
func previewIdentityFromStack(projectID, stackName string) (identity string, ok bool) {
	prefix := projectID + previewStackInfix
	if !strings.HasPrefix(stackName, prefix) {
		return "", false
	}
	identity = strings.TrimPrefix(stackName, prefix)
	if identity == "" {
		return "", false
	}
	return identity, true
}
