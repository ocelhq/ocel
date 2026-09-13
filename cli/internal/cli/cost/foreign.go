package cost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/pricing"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/pkg/costkit/pulumi"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

const (
	sourceOcel = providerkit.CostSource

	sstConfigFile = "sst.config.ts"
	sstStageFile  = ".sst/stage"
)

var pulumiProjectFiles = []string{"Pulumi.yaml", "Pulumi.yml"}

func sourceIn(dir, typed string) (string, error) {
	switch typed {
	case sourceOcel, pulumi.SourcePulumi, pulumi.SourceSST:
		return typed, nil
	case "":
	default:
		return "", fmt.Errorf("the source to price is %s, %s or %s, not %q", sourceOcel, pulumi.SourcePulumi, pulumi.SourceSST, typed)
	}
	for _, name := range []string{projectconfig.DefaultFileName, projectconfig.TSFileName} {
		if holds(dir, name) {
			return sourceOcel, nil
		}
	}
	if holds(dir, sstConfigFile) {
		return pulumi.SourceSST, nil
	}
	for _, name := range pulumiProjectFiles {
		if holds(dir, name) {
			return pulumi.SourcePulumi, nil
		}
	}
	return "", fmt.Errorf("%s holds none of %s, %s or %s, so there is nothing to price; pass --source %s, %s or %s to say which it is",
		dir, projectconfig.DefaultFileName, sstConfigFile, pulumiProjectFiles[0], sourceOcel, pulumi.SourcePulumi, pulumi.SourceSST)
}

func holds(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
	return err == nil
}

func runForeign(ctx context.Context, deps cmddeps.Deps, cwd string, opts Options, source string, profile costv1.Profile, overrides map[string]*structpb.Struct, stdout, stderr io.Writer) error {
	if opts.Env != "" {
		return fmt.Errorf("a %s project has no --env to price: stacks and stages are the environment, so name one with --%s", source, stackFlag(source))
	}
	set, err := inventory(ctx, deps, cwd, source, opts, stderr)
	if err != nil {
		return err
	}
	client := pricing.New(pricing.ResolveBaseURL(opts.PricingURL), strings.TrimSpace(os.Getenv(pricing.TokenEnvVar)))
	estimates, err := priceEveryProfile(ctx, client, set, overrides)
	if err != nil {
		return err
	}
	if deps.Presentation(stdout).Format == runui.FormatJSON {
		return writeJSON(stdout, set, estimates[profile], nil)
	}
	return render(stdout, header(source, set), set, estimates, profile, nil)
}

func priceEveryProfile(ctx context.Context, client costv1connect.CostServiceClient, set *costv1.ResourceSet, overrides map[string]*structpb.Struct) (map[costv1.Profile]*costv1.Estimate, error) {
	estimates := make(map[costv1.Profile]*costv1.Estimate, len(profiles()))
	for _, held := range profiles() {
		estimate, err := client.Price(ctx, &costv1.PriceRequest{
			Resources: set,
			Usage:     &costv1.Usage{Profile: held, Resources: overrides},
		})
		if err != nil {
			return nil, err
		}
		estimates[held] = estimate
	}
	return estimates, nil
}

func header(source string, set *costv1.ResourceSet) string {
	root := set.GetScopes()[0]
	return source + " " + root.GetKind() + " " + root.GetName()
}

func stackFlag(source string) string {
	if source == pulumi.SourceSST {
		return "stage"
	}
	return "stack"
}

func inventory(ctx context.Context, deps cmddeps.Deps, cwd, source string, opts Options, stderr io.Writer) (*costv1.ResourceSet, error) {
	if opts.From != "" {
		raw, err := os.ReadFile(opts.From)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", opts.From, err)
		}
		return pulumi.Parse(raw, pulumi.Options{Source: source, Name: named(opts, source)})
	}
	if source == pulumi.SourceSST {
		return sstInventory(ctx, deps, cwd, opts, stderr)
	}
	return pulumiInventory(ctx, deps, cwd, opts, stderr)
}

func named(opts Options, source string) string {
	if source == pulumi.SourceSST {
		return opts.Stage
	}
	return opts.Stack
}

func pulumiInventory(ctx context.Context, deps cmddeps.Deps, cwd string, opts Options, stderr io.Writer) (*costv1.ResourceSet, error) {
	stack := opts.Stack
	if stack == "" {
		named, err := run(ctx, deps, cwd, stderr, "pulumi", "stack", "--show-name")
		if err != nil {
			return nil, err
		}
		stack = strings.TrimSpace(string(named))
	}
	raw, err := run(ctx, deps, cwd, stderr, "pulumi", "preview", "--json", "--stack", stack)
	if err != nil {
		return nil, err
	}
	return pulumi.Parse(raw, pulumi.Options{Source: pulumi.SourcePulumi, Name: stack})
}

func sstInventory(ctx context.Context, deps cmddeps.Deps, cwd string, opts Options, stderr io.Writer) (*costv1.ResourceSet, error) {
	stage, err := stageIn(cwd, opts.Stage)
	if err != nil {
		return nil, err
	}
	state, err := run(ctx, deps, cwd, stderr, "sst", "state", "export", "--stage", stage)
	if err != nil {
		return nil, err
	}
	diff, err := run(ctx, deps, cwd, stderr, "sst", "diff", "--json", "--stage", stage)
	if err != nil {
		return nil, err
	}
	return pulumi.Merge(state, diff, pulumi.Options{Source: pulumi.SourceSST, Name: stage})
}

func stageIn(cwd, typed string) (string, error) {
	if typed != "" {
		return typed, nil
	}
	raw, err := os.ReadFile(filepath.Join(cwd, filepath.FromSlash(sstStageFile)))
	if err != nil {
		return "", fmt.Errorf("this project does not say which stage it is on in %s, so name one with --stage", sstStageFile)
	}
	stage := strings.TrimSpace(string(raw))
	if stage == "" {
		return "", fmt.Errorf("%s is empty, so name the stage to price with --stage", sstStageFile)
	}
	return stage, nil
}

func run(ctx context.Context, deps cmddeps.Deps, cwd string, stderr io.Writer, argv ...string) ([]byte, error) {
	out, err := deps.RunTool(ctx, cwd, argv, stderr)
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("%s is not on PATH, so this cannot ask it what a deploy would stand up; install it, or pass --from with the JSON it writes", argv[0])
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return out, nil
}
