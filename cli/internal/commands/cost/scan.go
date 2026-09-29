package cost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/internal/variablescope"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	envProduction = "production"
	envPreview    = "preview"

	defaultProfile = costv1.Profile_PROFILE_MODERATE
	profilePrefix  = "PROFILE_"

	unbuiltAssumption = "nothing is built, so each serverless app is priced as one function; run `ocel build` first to price the functions the build produces"
)

func profiles() []costv1.Profile {
	all := make([]costv1.Profile, 0, len(costv1.Profile_name)-1)
	for _, value := range slices.Sorted(maps.Keys(costv1.Profile_name)) {
		if profile := costv1.Profile(value); profile != costv1.Profile_PROFILE_UNSPECIFIED {
			all = append(all, profile)
		}
	}
	return all
}

func profileName(profile costv1.Profile) string {
	return strings.ToLower(strings.TrimPrefix(profile.String(), profilePrefix))
}

func profileNames() []string {
	names := make([]string, 0, len(profiles()))
	for _, profile := range profiles() {
		names = append(names, profileName(profile))
	}
	return names
}

type Options struct {
	Env     string
	Profile string
	Usage   string
}

func Run(ctx context.Context, dependencies Dependencies, cwd string, opts Options, stdout io.Writer) (err error) {
	env, err := environmentOf(opts.Env)
	if err != nil {
		return err
	}
	profile, err := profileOf(opts.Profile)
	if err != nil {
		return err
	}
	overrides, err := usageFile(opts.Usage)
	if err != nil {
		return err
	}
	cfg, err := dependencies.LoadProject(ctx, cwd)
	if err != nil {
		return err
	}

	if _, err := cfg.RequireProvider(); err != nil {
		return err
	}

	ctx, run, err := dependencies.Events.Begin(ctx, "ocel cost scan", cfg.Dir)
	if err != nil {
		return err
	}
	defer run.End(&err)

	check := run.Phase(progressv1.Phase_PHASE_CHECK)
	opened, _, err := dependencies.OpenProvider(ctx, check, cfg, commands.OpenOptions{})
	check.End(err)
	if err != nil {
		return err
	}
	defer opened.Close()

	pricing := run.Phase(progressv1.Phase_PHASE_PLAN).Unit(cfg.Slug, progress.Pricing.Title("what a deploy would provision"))
	set, estimates, assumptions, err := price(ctx, dependencies, opened, cfg, env, overrides, pricing.Output(progressv1.Level_LEVEL_INFO, progressv1.Stream_STREAM_UNSPECIFIED))
	pricing.End(err)
	if err != nil {
		return err
	}
	if dependencies.Presentation(stdout).Format == terminal.FormatJSON {
		return writeJSON(stdout, set, estimates[profile], assumptions)
	}
	return render(stdout, cfg.Slug, set, estimates, profile, assumptions)
}

func price(ctx context.Context, dependencies Dependencies, opened *providerprocess.Provider, cfg *project.Project, env *environmentv1.Environment, overrides map[string]*structpb.Struct, out io.Writer) (*costv1.ResourceSet, map[costv1.Profile]*costv1.Estimate, []string, error) {
	if !opened.Facts().GetPricesDeploys() {
		return nil, nil, nil, fmt.Errorf("%s does not price a deploy, so there is nothing to scan", opened.Name())
	}
	resolved, err := readiness.ResolveComputes(ctx, opened, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	manifest, assumptions, err := scanManifest(ctx, dependencies, resolved, env, out)
	if err != nil {
		return nil, nil, nil, err
	}
	var set *costv1.ResourceSet
	err = opened.Call(ctx, func(client contractv1connect.ProviderServiceClient) (err error) {
		set, err = client.Shape(ctx, &contractv1.ShapeRequest{
			Manifest:    manifest,
			Environment: env,
			Edge:        cfg.EdgeSelection(),
		})
		return err
	})
	if err != nil {
		return nil, nil, nil, err
	}
	costs, err := opened.Cost()
	if err != nil {
		return nil, nil, nil, err
	}
	estimates := make(map[costv1.Profile]*costv1.Estimate, len(profiles()))
	for _, profile := range profiles() {
		estimate, err := costs.Price(ctx, &costv1.PriceRequest{
			Resources: set,
			Usage:     &costv1.Usage{Profile: profile, Resources: overrides},
		})
		if err != nil {
			return nil, nil, nil, err
		}
		estimates[profile] = estimate
	}
	return set, estimates, assumptions, nil
}

func environmentOf(name string) (*environmentv1.Environment, error) {
	switch name {
	case "", envProduction:
		return &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION}, nil
	case envPreview:
		return &environmentv1.Environment{
			Tier:      environmentv1.Tier_TIER_PREVIEW,
			Lifecycle: environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL,
			Identity:  envPreview,
		}, nil
	}
	return nil, fmt.Errorf("the environment to price is %s or %s, not %q", envProduction, envPreview, name)
}

func profileOf(name string) (costv1.Profile, error) {
	if name == "" {
		return defaultProfile, nil
	}
	for _, profile := range profiles() {
		if profileName(profile) == name {
			return profile, nil
		}
	}
	return costv1.Profile_PROFILE_UNSPECIFIED, fmt.Errorf("the usage profile is one of %s, not %q", strings.Join(profileNames(), ", "), name)
}

func usageFile(path string) (map[string]*structpb.Struct, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read the usage file: %w", err)
	}
	var byResource map[string]map[string]any
	if err := yaml.Unmarshal(raw, &byResource); err != nil {
		return nil, fmt.Errorf("the usage file %s is neither YAML nor JSON keyed by resource id: %w", path, err)
	}
	overrides := make(map[string]*structpb.Struct, len(byResource))
	for id, quantities := range byResource {
		usage, err := structpb.NewStruct(quantities)
		if err != nil {
			return nil, fmt.Errorf("the usage file %s sets %s to something that is not a quantity: %w", path, id, err)
		}
		overrides[id] = usage
	}
	return overrides, nil
}

type unread struct{}

func (unread) List(context.Context) ([]variables.ValueMetadata, error) { return nil, nil }

func (unread) Reveal(context.Context, []variables.Coordinate) (map[variables.Coordinate]string, error) {
	return map[variables.Coordinate]string{}, nil
}

const unbuiltDigest = "0000000000000000000000000000000000000000000000000000000000000000"

func scanManifest(ctx context.Context, dependencies Dependencies, cfg *project.Project, env *environmentv1.Environment, out io.Writer) (*contractv1.Manifest, []string, error) {
	declarations := variables.NewDeclarations(unread{}, variablescope.Of(cfg, env.GetTier(), ""))
	resources, err := dependencies.CollectDeclarations(ctx, cfg, declarations, out, out)
	if err != nil {
		return nil, nil, err
	}
	built, assumptions, err := scannedOutput(dependencies, cfg)
	if err != nil {
		return nil, nil, err
	}
	scanned, err := manifest.Assemble(manifest.Input{
		Project:   cfg,
		Tier:      env.GetTier(),
		Resources: resources,
		Built:     built,
	})
	if err != nil {
		return nil, nil, err
	}
	return scanned, assumptions, nil
}

func scannedOutput(dependencies Dependencies, cfg *project.Project) (build.Output, []string, error) {
	images := make(map[string]string, len(cfg.Apps))
	for _, a := range cfg.Apps {
		if a.RunsOn(provider.ComputeContainer) {
			images[a.Name] = cfg.Slug + "/" + a.Name + "@sha256:" + unbuiltDigest
		}
	}
	functions, err := dependencies.ReadFunctions(cfg.Dir)
	if errors.Is(err, build.ErrNoBuildOutput) {
		return build.Output{Functions: unbuiltFunctions(cfg), Images: images}, []string{unbuiltAssumption}, nil
	}
	if err != nil {
		return build.Output{}, nil, err
	}
	return build.Output{Functions: functions, Images: images}, nil, nil
}

func unbuiltFunctions(cfg *project.Project) []build.Function {
	functions := make([]build.Function, 0, len(cfg.Apps))
	for _, a := range build.FunctionApps(cfg.Apps) {
		functions = append(functions, build.Function{Route: a.Name, App: a.Name, Framework: buildoutput.Framework{Name: a.Framework(), Arch: a.Arch}})
	}
	return functions
}

func writeJSON(stdout io.Writer, set *costv1.ResourceSet, estimate *costv1.Estimate, assumptions []string) error {
	resources, err := protojson.Marshal(set)
	if err != nil {
		return err
	}
	priced, err := protojson.Marshal(estimate)
	if err != nil {
		return err
	}
	if assumptions == nil {
		assumptions = []string{}
	}
	encoded, err := json.Marshal(struct {
		Resources   json.RawMessage `json:"resources"`
		Estimate    json.RawMessage `json:"estimate"`
		Assumptions []string        `json:"assumptions"`
	}{resources, priced, assumptions})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(encoded))
	return err
}
